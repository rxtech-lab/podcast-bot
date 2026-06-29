package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

const (
	summaryPPTTemplateVersion = "3"
	summaryPPTExportTimeout   = 2 * time.Minute
)

var (
	errSummaryDeckUnavailable     = errors.New("summary slide deck is not available")
	errSummaryDeckGenerating      = errors.New("summary slide deck is already generating")
	errSummaryPPTXNotConfigured   = errors.New("summary PPTX export is not configured")
	errSummaryPPTPDFNotConfigured = errors.New("summary PPT PDF export is not configured")
)

func (s *Server) handleDiscussionSummaryPPTX(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	visible, doc, err := s.summaryDeckDocumentForExport(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		s.writeSummaryDeckExportError(w, err)
		return
	}
	if visible == nil || doc == nil {
		http.NotFound(w, r)
		return
	}

	pptx, err := s.summaryPPTXBytes(r.Context(), visible.ID, doc)
	if err != nil {
		s.writeSummaryDeckExportError(w, err)
		return
	}
	s.writeSummaryFile(w, summaryExportTitle(visible), "pptx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx)
}

func (s *Server) handleDiscussionSummaryPPTPDF(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	visible, doc, err := s.summaryDeckDocumentForExport(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		s.writeSummaryDeckExportError(w, err)
		return
	}
	if visible == nil || doc == nil {
		http.NotFound(w, r)
		return
	}

	pdf, err := s.summaryPPTPDFBytes(r.Context(), visible.ID, doc)
	if err != nil {
		s.writeSummaryDeckExportError(w, err)
		return
	}
	s.writeSummaryFile(w, summaryExportTitle(visible), "pdf", "application/pdf", pdf)
}

func (s *Server) summaryDeckDocumentForExport(ctx context.Context, userID, discussionID string) (*Discussion, *SummaryDocument, error) {
	visible, err := s.d.Discussions.GetVisible(ctx, userID, discussionID)
	if err != nil {
		return nil, nil, err
	}
	if visible == nil {
		return nil, nil, nil
	}
	doc, err := s.d.Discussions.SummaryDocumentFor(ctx, discussionID, SummaryDocTypePPT)
	if err != nil {
		return nil, nil, err
	}
	if doc != nil && doc.Status == SummaryReadyState && strings.TrimSpace(doc.Markdown) != "" {
		return visible, doc, nil
	}
	if doc != nil && doc.Status == SummaryGenerating {
		return visible, nil, errSummaryDeckGenerating
	}
	if !visible.IsOwner {
		return visible, nil, errSummaryDeckUnavailable
	}
	doc, err = s.generateSummaryDeckDocument(ctx, visible)
	if err != nil {
		return visible, nil, err
	}
	return visible, doc, nil
}

func (s *Server) generateSummaryDeckDocument(ctx context.Context, d *Discussion) (*SummaryDocument, error) {
	if d == nil {
		return nil, errSummaryDeckUnavailable
	}
	source, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeSummary)
	if err != nil {
		return nil, err
	}
	if source == nil || source.Status != SummaryReadyState || strings.TrimSpace(source.Markdown) == "" {
		return nil, errSummaryDeckUnavailable
	}

	gen := summarizer.NewDeckGenerator(s.d.Env)
	model := gen.Model()
	meter := &summaryUsageMeter{}
	runner := gen.WithUsageRecorder(meter.record)
	if runner == nil {
		return nil, ErrSummaryNotConfigured
	}
	if err := s.d.Discussions.BeginSummary(ctx, d.ID, SummaryDocTypePPT, model); err != nil {
		return nil, err
	}

	spec, err := runner.Generate(ctx, summarizer.DeckInput{
		Title:           summaryExportTitle(d),
		Topic:           d.Topic,
		Language:        d.Language,
		SummaryMarkdown: source.Markdown,
	})
	if err != nil {
		_ = s.d.Discussions.FailSummary(ctx, d.ID, SummaryDocTypePPT, err.Error())
		return nil, err
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		_ = s.d.Discussions.FailSummary(ctx, d.ID, SummaryDocTypePPT, err.Error())
		return nil, err
	}
	sum := meter.snapshot()
	if err := s.d.Discussions.SaveSummary(ctx, d.ID, SummaryDocTypePPT, string(data), model, SummaryUsage{
		PromptTokens:     sum.PromptTokens,
		CompletionTokens: sum.CompletionTokens,
		TotalTokens:      sum.TotalTokens,
		LLMCostUSD:       sum.CostUSD,
	}); err != nil {
		_ = s.d.Discussions.FailSummary(ctx, d.ID, SummaryDocTypePPT, "failed to store slide deck")
		return nil, err
	}
	return s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypePPT)
}

func (s *Server) summaryPPTXBytes(ctx context.Context, discussionID string, doc *SummaryDocument) ([]byte, error) {
	cacheKey := s.summaryDeckCacheKey(discussionID, doc, "pptx")
	if cacheKey != "" {
		if info, err := s.d.Uploader.Head(ctx, cacheKey); err == nil && info.ContentLength > 0 {
			if data, err := s.d.Uploader.Download(ctx, cacheKey); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}

	pptx, err := summaryPPTXFromDeckJSON(ctx, s.d.Env, doc.Markdown)
	if err != nil {
		return nil, err
	}
	if cacheKey != "" {
		if err := s.d.Uploader.UploadBytes(ctx, cacheKey,
			"application/vnd.openxmlformats-officedocument.presentationml.presentation", pptx); err != nil {
			s.logger().Warn("summary pptx cache upload failed", "discussion", discussionID, "err", err)
		}
	}
	return pptx, nil
}

func (s *Server) summaryPPTPDFBytes(ctx context.Context, discussionID string, doc *SummaryDocument) ([]byte, error) {
	cacheKey := s.summaryDeckCacheKey(discussionID, doc, "pdf")
	if cacheKey != "" {
		if info, err := s.d.Uploader.Head(ctx, cacheKey); err == nil && info.ContentLength > 0 {
			if data, err := s.d.Uploader.Download(ctx, cacheKey); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}

	pptx, err := s.summaryPPTXBytes(ctx, discussionID, doc)
	if err != nil {
		return nil, err
	}
	pdf, err := summaryPPTPDFFromPPTX(ctx, s.d.Env, pptx)
	if err != nil {
		return nil, err
	}
	if cacheKey != "" {
		if err := s.d.Uploader.UploadBytes(ctx, cacheKey, "application/pdf", pdf); err != nil {
			s.logger().Warn("summary ppt pdf cache upload failed", "discussion", discussionID, "err", err)
		}
	}
	return pdf, nil
}

func (s *Server) summaryDeckCacheKey(discussionID string, doc *SummaryDocument, ext string) string {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() || doc == nil {
		return ""
	}
	var gen int64
	if doc.GeneratedAt != nil {
		gen = doc.GeneratedAt.Unix()
	}
	return s.d.Uploader.Key(fmt.Sprintf("summary-ppt/%s/%s-v%s-%d.%s",
		discussionID, SummaryDocTypePPT, summaryPPTTemplateVersion, gen, ext))
}

func summaryPPTXFromDeckJSON(ctx context.Context, env *config.Env, deckJSON string) ([]byte, error) {
	script := pptxRendererScript(env)
	if script == "" {
		return nil, errSummaryPPTXNotConfigured
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil, errSummaryPPTXNotConfigured
	}
	if _, err := os.Stat(script); err != nil {
		return nil, errSummaryPPTXNotConfigured
	}
	tmp, err := os.MkdirTemp("", "summary-ppt-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	inputPath := filepath.Join(tmp, "deck.json")
	outputPath := filepath.Join(tmp, "deck.pptx")
	if err := os.WriteFile(inputPath, []byte(deckJSON), 0o600); err != nil {
		return nil, err
	}
	cmdCtx, cancel := context.WithTimeout(ctx, summaryPPTExportTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "node", script, inputPath, outputPath)
	cmd.Dir = filepath.Dir(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("summary pptx render failed: %w: %s", err, truncateText(string(out), 600))
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered pptx: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("summary pptx render produced an empty file")
	}
	return data, nil
}

func summaryPPTPDFFromPPTX(ctx context.Context, env *config.Env, pptx []byte) ([]byte, error) {
	soffice, err := libreOfficePath(env)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "summary-ppt-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	inputPath := filepath.Join(tmp, "deck.pptx")
	if err := os.WriteFile(inputPath, pptx, 0o600); err != nil {
		return nil, err
	}
	cmdCtx, cancel := context.WithTimeout(ctx, summaryPPTExportTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, soffice, "--headless", "--convert-to", "pdf", "--outdir", tmp, inputPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("summary ppt pdf render failed: %w: %s", err, truncateText(string(out), 600))
	}
	outputPath := filepath.Join(tmp, "deck.pdf")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered ppt pdf: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("summary ppt pdf render produced an empty file")
	}
	return data, nil
}

func pptxRendererScript(env *config.Env) string {
	if env != nil && strings.TrimSpace(env.PPTXRendererScript) != "" {
		return absPath(strings.TrimSpace(env.PPTXRendererScript))
	}
	for _, candidate := range []string{
		filepath.Join("tools", "ppt-renderer", "render.mjs"),
		filepath.Join("/app", "tools", "ppt-renderer", "render.mjs"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return absPath(candidate)
		}
	}
	return ""
}

func absPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func libreOfficePath(env *config.Env) (string, error) {
	if env != nil && strings.TrimSpace(env.LibreOfficePath) != "" {
		return strings.TrimSpace(env.LibreOfficePath), nil
	}
	for _, name := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	const macPath = "/Applications/LibreOffice.app/Contents/MacOS/soffice"
	if _, err := os.Stat(macPath); err == nil {
		return macPath, nil
	}
	return "", errSummaryPPTPDFNotConfigured
}

func summaryExportTitle(d *Discussion) string {
	if d == nil {
		return "Summary"
	}
	if title := strings.TrimSpace(d.Title); title != "" {
		return title
	}
	if topic := strings.TrimSpace(d.Topic); topic != "" {
		return topic
	}
	return "Summary"
}

func (s *Server) writeSummaryDeckExportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSummaryDeckUnavailable):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errSummaryDeckGenerating):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrSummaryNotConfigured), errors.Is(err, errSummaryPPTXNotConfigured), errors.Is(err, errSummaryPPTPDFNotConfigured):
		s.logger().Warn("summary deck export unavailable", "err", err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		s.logger().Error("summary deck export failed", "err", err)
		http.Error(w, "failed to export summary slide deck", http.StatusBadGateway)
	}
}

func (s *Server) writeSummaryFile(w http.ResponseWriter, title, ext, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", summaryExportFilename(title, ext)))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func summaryExportFilename(title, ext string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "Summary"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.TrimSpace(name)
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "Summary"
	}
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if ext == "" {
		ext = "bin"
	}
	return name + "." + ext
}
