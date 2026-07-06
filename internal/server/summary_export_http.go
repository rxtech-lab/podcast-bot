package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirily11/debate-bot/internal/mq"
)

// Async export documents: rows in the summaries doc-store that track the
// queued render of a downloadable artifact. The artifact bytes live in
// object storage under the same deterministic cache keys the synchronous
// GET endpoints use, so a finished export is served instantly by the
// existing download routes; the export row is the status the client
// polls / receives SSE events for.
const (
	SummaryDocTypeExportPPTX   = "export-pptx"
	SummaryDocTypeExportPPTPDF = "export-ppt-pdf"
	SummaryDocTypeExportPDF    = "export-pdf"
)

// Export kinds carried in the task payload.
const (
	summaryExportKindPPTX   = "pptx"
	summaryExportKindPPTPDF = "ppt-pdf"
	summaryExportKindPDF    = "pdf"
)

// SummaryExportPayload is the wire payload of a queued summary export.
type SummaryExportPayload struct {
	DiscussionID string `json:"discussion_id"`
	Kind         string `json:"kind"`
}

// summaryExportArtifact is what a ready export row stores in the markdown
// column: where the rendered artifact lives.
type summaryExportArtifact struct {
	S3Key       string `json:"s3_key"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
}

// SummaryExportDocTypeFor maps an export kind to its doc-store row type
// (for the dispatch layer's claim).
func SummaryExportDocTypeFor(kind string) (string, error) {
	docType, _, err := summaryExportDocType(kind)
	return docType, err
}

func summaryExportDocType(kind string) (string, mq.TaskType, error) {
	switch kind {
	case summaryExportKindPPTX:
		return SummaryDocTypeExportPPTX, mq.TaskPPTExport, nil
	case summaryExportKindPPTPDF:
		return SummaryDocTypeExportPPTPDF, mq.TaskPPTExport, nil
	case summaryExportKindPDF:
		return SummaryDocTypeExportPDF, mq.TaskPDFExport, nil
	default:
		return "", "", fmt.Errorf("unknown export kind %q", kind)
	}
}

// handleDiscussionSummaryExport enqueues an async export render (kind from
// the route) and returns the export row's meta. Idempotent: an export
// already generating or ready returns its current state without re-queueing.
func (s *Server) handleDiscussionSummaryExport(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := s.requestUser(r)
		id := r.PathValue("id")
		docType, taskType, err := summaryExportDocType(kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		visible, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if visible == nil {
			http.NotFound(w, r)
			return
		}
		if s.d.Uploader == nil || !s.d.Uploader.Enabled() || s.d.MQ == nil {
			http.Error(w, "async export is not configured", http.StatusServiceUnavailable)
			return
		}
		// PPT-family exports may need to generate the deck document first,
		// which only the owner may trigger (mirrors the synchronous path).
		if kind != summaryExportKindPDF && !visible.IsOwner {
			if doc, derr := s.d.Discussions.SummaryDocumentFor(r.Context(), id, SummaryDocTypePPT); derr != nil || doc == nil || doc.Status != SummaryReadyState {
				http.Error(w, errSummaryDeckUnavailable.Error(), http.StatusNotFound)
				return
			}
		}
		// The PDF export renders the summary document; require it ready.
		if kind == summaryExportKindPDF {
			doc, derr := s.d.Discussions.SummaryDocumentFor(r.Context(), id, SummaryDocTypeSummary)
			if derr != nil {
				http.Error(w, derr.Error(), http.StatusInternalServerError)
				return
			}
			if doc == nil || doc.Status != SummaryReadyState || strings.TrimSpace(doc.Markdown) == "" {
				http.Error(w, "summary is not available for PDF export", http.StatusConflict)
				return
			}
		}

		// Idempotence: don't re-enqueue an export that is already pending or
		// done — return its state so the client can resume waiting/download.
		if status, exists, err := s.d.Discussions.SummaryStatusFor(r.Context(), id, docType); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if exists && (status == SummaryGenerating || status == SummaryReadyState) {
			meta, merr := s.d.Discussions.SummaryMetaFor(r.Context(), id, docType)
			if merr != nil {
				http.Error(w, merr.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, meta)
			return
		}

		if err := s.d.Discussions.BeginSummary(r.Context(), id, docType, ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		task, err := mq.NewTask(taskType, id, SummaryExportPayload{DiscussionID: id, Kind: kind})
		if err == nil {
			err = s.d.MQ.Publish(r.Context(), mq.QueueDocs, task)
		}
		if err != nil {
			_ = s.d.Discussions.FailSummary(r.Context(), id, docType, "failed to enqueue export")
			s.logger().Error("summary export enqueue failed", "discussion", id, "kind", kind, "err", err)
			http.Error(w, "failed to enqueue export", http.StatusInternalServerError)
			return
		}
		s.publishSummaryExportEvent(visible.JobID, id, docType, string(SummaryGenerating))
		meta, err := s.d.Discussions.SummaryMetaFor(r.Context(), id, docType)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, meta)
	}
}

func (s *Server) publishSummaryExportEvent(jobID, discussionID, docType, status string) {
	publishSummaryEvent(SummaryGenerationDeps{
		Env:         s.d.Env,
		Bus:         s.d.Bus,
		Discussions: s.d.Discussions,
		Log:         s.logger(),
	}, jobID, discussionID, docType, status)
}

// RunSummaryExportTask executes one queued export attempt: render the
// artifact (generating the slide deck first when needed), upload it to the
// deterministic cache key the download endpoints read, and mark the export
// row ready. The returned error is the attempt's failure; the dispatch
// layer owns retry vs terminal.
func (s *Server) RunSummaryExportTask(ctx context.Context, p SummaryExportPayload) error {
	docType, _, err := summaryExportDocType(p.Kind)
	if err != nil {
		return mq.Permanent(err)
	}
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return mq.Permanent(errors.New("async export requires object storage"))
	}
	d, err := s.d.Discussions.GetForNotification(ctx, p.DiscussionID)
	if err != nil {
		return fmt.Errorf("load discussion: %w", err)
	}
	if d == nil {
		return mq.Permanent(fmt.Errorf("discussion %s not found", p.DiscussionID))
	}

	var artifact summaryExportArtifact
	switch p.Kind {
	case summaryExportKindPPTX, summaryExportKindPPTPDF:
		deckDoc, derr := s.exportDeckDocument(ctx, d)
		if derr != nil {
			return derr
		}
		if p.Kind == summaryExportKindPPTX {
			data, rerr := s.summaryPPTXBytes(ctx, d.ID, deckDoc)
			if rerr != nil {
				return exportRenderError(rerr)
			}
			artifact = summaryExportArtifact{
				S3Key:       s.summaryDeckCacheKey(d.ID, deckDoc, "pptx"),
				Size:        len(data),
				ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			}
		} else {
			data, rerr := s.summaryPPTPDFBytes(ctx, d.ID, deckDoc)
			if rerr != nil {
				return exportRenderError(rerr)
			}
			artifact = summaryExportArtifact{
				S3Key:       s.summaryDeckCacheKey(d.ID, deckDoc, "pdf"),
				Size:        len(data),
				ContentType: "application/pdf",
			}
		}
	case summaryExportKindPDF:
		doc, derr := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeSummary)
		if derr != nil {
			return derr
		}
		if doc == nil || doc.Status != SummaryReadyState || strings.TrimSpace(doc.Markdown) == "" {
			return mq.Permanent(errors.New("summary is not available for PDF export"))
		}
		title := summaryExportTitle(d)
		pdf, rerr := summaryPDFFromMarkdown(ctx, s.d.Env, title, s.summaryMarkdownWithLink(d.ID, doc.Markdown))
		if rerr != nil {
			return exportRenderError(rerr)
		}
		cacheKey := s.summaryPDFCacheKey(d.ID, doc)
		if cacheKey == "" {
			return mq.Permanent(errors.New("summary pdf cache key unavailable"))
		}
		if uerr := s.d.Uploader.UploadBytes(ctx, cacheKey, "application/pdf", pdf); uerr != nil {
			return fmt.Errorf("upload summary pdf: %w", uerr)
		}
		artifact = summaryExportArtifact{S3Key: cacheKey, Size: len(pdf), ContentType: "application/pdf"}
	}

	blob, err := json.Marshal(artifact)
	if err != nil {
		return mq.Permanent(fmt.Errorf("encode export artifact: %w", err))
	}
	if err := s.d.Discussions.SaveSummary(ctx, d.ID, docType, string(blob), "", SummaryUsage{}); err != nil {
		return fmt.Errorf("store export row: %w", err)
	}
	s.publishSummaryExportEvent(d.JobID, d.ID, docType, string(SummaryReadyState))
	return nil
}

// FailSummaryExportTask records the terminal failure of a queued export.
func (s *Server) FailSummaryExportTask(p SummaryExportPayload, cause error) {
	docType, _, err := summaryExportDocType(p.Kind)
	if err != nil {
		return
	}
	msg := "export failed"
	if cause != nil {
		msg = cause.Error()
	}
	ctx := context.Background()
	_ = s.d.Discussions.FailSummary(ctx, p.DiscussionID, docType, msg)
	jobID := ""
	if d, derr := s.d.Discussions.GetForNotification(ctx, p.DiscussionID); derr == nil && d != nil {
		jobID = d.JobID
	}
	s.publishSummaryExportEvent(jobID, p.DiscussionID, docType, string(SummaryFailed))
}

// exportDeckDocument returns the ready slide-deck document, generating it
// when absent (the queued export runs with the owner's authority — the POST
// handler already enforced who may trigger deck generation).
func (s *Server) exportDeckDocument(ctx context.Context, d *Discussion) (*SummaryDocument, error) {
	doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypePPT)
	if err != nil {
		return nil, err
	}
	if doc != nil && doc.Status == SummaryReadyState && strings.TrimSpace(doc.Markdown) != "" {
		return doc, nil
	}
	if doc != nil && doc.Status == SummaryGenerating {
		// Another export (or a synchronous request) is generating the deck
		// right now; retry after the backoff instead of double-generating.
		return nil, errSummaryDeckGenerating
	}
	doc, err = s.generateSummaryDeckDocument(ctx, d)
	if err != nil {
		return nil, exportRenderError(err)
	}
	return doc, nil
}

// exportRenderError classifies render failures: configuration gaps can
// never succeed on retry, everything else (network, renderer flake) can.
func exportRenderError(err error) error {
	switch {
	case errors.Is(err, errSummaryPPTXNotConfigured),
		errors.Is(err, errSummaryPPTPDFNotConfigured),
		errors.Is(err, errCloudflareNotConfigured),
		errors.Is(err, ErrSummaryNotConfigured),
		errors.Is(err, errSummaryDeckUnavailable):
		return mq.Permanent(err)
	default:
		return err
	}
}
