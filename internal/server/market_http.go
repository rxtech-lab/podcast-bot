package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

const (
	stationCoverImageModel   = "google/gemini-3.1-flash-image"
	stationCoverImageSize    = "1024x1024"
	stationCoverImageCostUSD = 0.067
	stationCoverURLTTL       = 30 * 24 * time.Hour

	// coverGenerationBackgroundTimeout bounds the fire-and-forget cover
	// generation kicked off at discussion creation.
	coverGenerationBackgroundTimeout = 5 * time.Minute
)

type marketVisibilityRequest struct {
	Visibility DiscussionVisibility `json:"visibility"`
	Cover      DiscussionCover      `json:"cover"`
}

type marketCoverGenerateRequest struct {
	Prompt string `json:"prompt"`
}

type marketCoverSetRequest struct {
	Cover DiscussionCover `json:"cover"`
}

type marketCoverGenerateResponse struct {
	Cover DiscussionCover `json:"cover"`
}

func (s *Server) handleMarketList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	items, err := s.d.Discussions.ListPublic(
		r.Context(),
		user.ID,
		strings.TrimSpace(r.URL.Query().Get("q")),
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, items)
	writeJSON(w, items)
}

func (s *Server) handleMarketLikedList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	items, err := s.d.Discussions.ListLiked(
		r.Context(),
		user.ID,
		strings.TrimSpace(r.URL.Query().Get("q")),
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, items)
	writeJSON(w, items)
}

func (s *Server) handleMarketGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Discussions.GetVisible(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d)
	s.applyDiscussionProgress(r.Context(), d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleMarketLike(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Discussions.Like(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d)
	s.applyDiscussionProgress(r.Context(), d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleMarketUnlike(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Discussions.Unlike(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d)
	s.applyDiscussionProgress(r.Context(), d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleDiscussionVisibility(w http.ResponseWriter, r *http.Request) {
	var req marketVisibilityRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	visibility := req.Visibility
	if visibility == "" {
		visibility = DiscussionPrivate
	}
	if visibility != DiscussionPrivate && visibility != DiscussionPublic {
		http.Error(w, "invalid visibility", http.StatusBadRequest)
		return
	}
	cover := req.Cover
	if visibility == DiscussionPrivate {
		cover = DiscussionCover{}
	}
	updated, err := s.d.Discussions.SetVisibility(r.Context(), s.requestUser(r).ID, r.PathValue("id"), visibility, cover)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "cover is required") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, updated)
	s.applyDiscussionProgress(r.Context(), updated)
	s.refreshDiscussionCoverURL(r.Context(), updated)
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

func (s *Server) handleDiscussionCoverGenerate(w http.ResponseWriter, r *http.Request) {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		http.Error(w, "cover generation requires S3 storage", http.StatusServiceUnavailable)
		return
	}
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	var req marketCoverGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Square podcast cover artwork for " + d.DisplayTitle()
	}

	reserved, reserveLedgerID, ok := s.reserveImageGeneration(w, r, user.ID, id)
	if !ok {
		return
	}
	cover, err := s.generateStationCover(r.Context(), user.ID, id, prompt)
	if err != nil {
		s.refundImageGeneration(r.Context(), user.ID, id, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.settleImageGeneration(r.Context(), user.ID, id, reserved, reserveLedgerID)
	writeJSON(w, marketCoverGenerateResponse{Cover: cover})
}

func (s *Server) prepareMarketDiscussions(r *http.Request, items []Discussion) {
	for i := range items {
		s.applyDiscussionJobStatus(r, &items[i])
		s.applyDiscussionProgress(r.Context(), &items[i])
		s.refreshDiscussionCoverURL(r.Context(), &items[i])
		s.sanitizeDiscussionUsage(&items[i])
	}
}

func (s *Server) refreshDiscussionCoverURL(ctx context.Context, d *Discussion) {
	if d == nil || strings.TrimSpace(d.Cover.ImageKey) == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return
	}
	url, err := s.d.Uploader.DownloadURL(ctx, d.Cover.ImageKey, stationCoverURLTTL)
	if err != nil {
		s.logger().Warn("cover download url failed", "discussion", d.ID, "err", err)
		return
	}
	d.Cover.ImageURL = url
}

func (d Discussion) DisplayTitle() string {
	if strings.TrimSpace(d.Title) != "" {
		return d.Title
	}
	if d.Script != nil && strings.TrimSpace(d.Script.Title) != "" {
		return d.Script.Title
	}
	return d.Topic
}

func (s *Server) reserveImageGeneration(w http.ResponseWriter, r *http.Request, userID, discID string) (int64, int64, bool) {
	if !s.pointsEnabled() {
		return 0, 0, true
	}
	required := pointsForUSD(s.d.Env, stationCoverImageCostUSD)
	if required <= 0 {
		return 0, 0, true
	}
	ok, bal, reserveLedgerID, err := s.d.Points.ReserveWithLedgerID(r.Context(), userID, discID, required, pointsReasonImageGeneration)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, 0, false
	}
	if !ok {
		writeInsufficientPoints(w, required, bal)
		return 0, 0, false
	}
	return required, reserveLedgerID, true
}

// reserveImageGenerationBackground is the context-only counterpart to
// reserveImageGeneration for fire-and-forget background generation: it has no
// ResponseWriter to report to, so insufficient points or errors simply yield
// ok=false and are logged by the caller.
func (s *Server) reserveImageGenerationBackground(ctx context.Context, userID, discID string) (int64, int64, bool) {
	if !s.pointsEnabled() {
		return 0, 0, true
	}
	required := pointsForUSD(s.d.Env, stationCoverImageCostUSD)
	if required <= 0 {
		return 0, 0, true
	}
	ok, bal, reserveLedgerID, err := s.d.Points.ReserveWithLedgerID(ctx, userID, discID, required, pointsReasonImageGeneration)
	if err != nil {
		s.logger().Warn("background cover reservation failed", "discussion", discID, "err", err)
		return 0, 0, false
	}
	if !ok {
		s.logger().Info("background cover generation skipped: insufficient points", "discussion", discID, "required", required, "balance", bal)
		return 0, 0, false
	}
	return required, reserveLedgerID, true
}

func (s *Server) settleImageGeneration(ctx context.Context, userID, discID string, reserved, reserveLedgerID int64) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	detail := PointsUsageDetail{CostUSD: stationCoverImageCostUSD}
	if _, err := s.d.Points.SettleReserved(ctx, userID, discID, reserveLedgerID, reserved, reserved, pointsReasonImageGeneration, detail); err != nil {
		s.logger().Warn("image generation settle failed", "discussion", discID, "err", err)
	}
}

func (s *Server) refundImageGeneration(ctx context.Context, userID, discID string, reserved, reserveLedgerID int64) {
	if !s.pointsEnabled() || reserved <= 0 {
		return
	}
	if _, err := s.d.Points.SettleReserved(ctx, userID, discID, reserveLedgerID, reserved, 0, pointsReasonImageGeneration, PointsUsageDetail{}); err != nil {
		s.logger().Warn("image generation refund failed", "discussion", discID, "err", err)
	}
}

func (s *Server) generateStationCover(ctx context.Context, userID, discID, prompt string) (DiscussionCover, error) {
	client, err := imagegen.New("")
	if err != nil {
		return DiscussionCover{}, err
	}
	raw, err := client.Generate(ctx, imagegen.Request{
		Model:  stationCoverImageModel,
		Prompt: stationCoverGenerationPrompt(prompt),
		Size:   stationCoverImageSize,
	})
	if err != nil {
		return DiscussionCover{}, err
	}
	// Image models return PNG/JPEG; re-encode to WebP so covers are small and
	// match the format the iOS upload path already produces.
	webp, err := imagegen.ToWebP(raw)
	if err != nil {
		return DiscussionCover{}, err
	}
	tmp, err := os.CreateTemp("", "station-cover-*.webp")
	if err != nil {
		return DiscussionCover{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(webp); err != nil {
		tmp.Close()
		return DiscussionCover{}, err
	}
	if err := tmp.Close(); err != nil {
		return DiscussionCover{}, err
	}
	key := s.d.Uploader.Key("covers/" + safeKeySegment(userID) + "/" + discID + filepath.Ext(tmpPath))
	if err := s.d.Uploader.Upload(ctx, tmpPath, key); err != nil {
		return DiscussionCover{}, err
	}
	url, err := s.d.Uploader.DownloadURL(ctx, key, stationCoverURLTTL)
	if err != nil {
		return DiscussionCover{}, err
	}
	return DiscussionCover{
		Type:     "ai",
		ImageKey: key,
		ImageURL: url,
		Prompt:   prompt,
	}, nil
}

func stationCoverGenerationPrompt(subject string) string {
	subject = strings.TrimSpace(subject)
	return fmt.Sprintf(`Create a square podcast cover image for this discussion:
%q

Design it as simple, flat podcast cover artwork: minimal visual elements, clean geometric layout, restrained color palette, crisp edges, and little to no shadows. Avoid busy scenes, realistic lighting, 3D effects, heavy textures, clutter, and detailed illustrations. Use clean modern typography only when it improves the cover. Do not write an essay, markdown, explanation, or article. Return only the generated cover image.`, subject)
}
