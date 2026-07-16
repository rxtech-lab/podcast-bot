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
	stationCoverImageModel   = "google/gemini-3.1-flash-lite-image"
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
	// Language targets a translation's dedicated cover instead of the default
	// one; it must name an existing, ready translation of the discussion.
	Language string `json:"language,omitempty"`
}

type marketCoverSetRequest struct {
	Cover DiscussionCover `json:"cover"`
	// Language persists the cover on that translation row instead of the
	// discussion itself; it must name an existing translation.
	Language string `json:"language,omitempty"`
}

type marketCoverGenerateResponse struct {
	Cover DiscussionCover `json:"cover"`
}

func (s *Server) handleMarketList(w http.ResponseWriter, r *http.Request) {
	timer := newStationTimer()
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	qStart := time.Now()
	items, err := s.d.Discussions.ListPublic(
		r.Context(),
		user.ID,
		strings.TrimSpace(r.URL.Query().Get("q")),
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, items, timer)
	writeJSON(w, items)
	s.logStationTiming("market.list", len(items), timer)
}

func (s *Server) handleMarketLikedList(w http.ResponseWriter, r *http.Request) {
	timer := newStationTimer()
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	qStart := time.Now()
	items, err := s.d.Discussions.ListLiked(
		r.Context(),
		user.ID,
		strings.TrimSpace(r.URL.Query().Get("q")),
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, items, timer)
	writeJSON(w, items)
	s.logStationTiming("market.liked", len(items), timer)
}

func (s *Server) handleMarketGet(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	d, err := s.d.Discussions.GetVisible(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d, true)
	s.applyDiscussionProgress(r.Context(), d)
	s.applyDiscussionTranslationPresentation(r, d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.refreshDiscussionLineAudioURLs(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	s.applyDiscussionShareURL(d)
	writeJSON(w, d)
}

func (s *Server) handleMarketLike(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	d, created, err := s.d.Discussions.LikeWithCreated(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d, true)
	s.applyDiscussionProgress(r.Context(), d)
	s.applyDiscussionTranslationPresentation(r, d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	s.applyDiscussionShareURL(d)
	if created && d.OwnerUserID != "" && d.OwnerUserID != user.ID {
		s.notifyMarketLike(r.Context(), d.OwnerUserID, d, userDisplayName(user))
	}
	writeJSON(w, d)
}

func (s *Server) handleMarketUnlike(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	d, err := s.d.Discussions.Unlike(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionJobStatus(r, d, true)
	s.applyDiscussionProgress(r.Context(), d)
	s.applyDiscussionTranslationPresentation(r, d)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	s.applyDiscussionShareURL(d)
	writeJSON(w, d)
}

func (s *Server) handleMarketProfile(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	profile, err := s.d.Discussions.CreatorProfile(r.Context(), user.ID, user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if profile == nil {
		profile = &CreatorProfile{ID: user.ID, DisplayName: userDisplayName(user), AvatarURL: strings.TrimSpace(user.AvatarURL), IsSelf: true}
	}
	timer := newStationTimer()
	qStart := time.Now()
	stations, err := s.d.Discussions.ListByCreator(
		r.Context(),
		user.ID,
		user.ID,
		"",
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, stations, timer)
	following, err := s.d.Discussions.ListFollowing(r.Context(), user.ID, defaultDiscussionPageSize, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, MarketProfile{Profile: *profile, Stations: stations, Following: following})
	s.logStationTiming("market.profile", len(stations), timer)
}

func (s *Server) handleMarketCreatorGet(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	profile, err := s.d.Discussions.CreatorProfile(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if profile == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, profile)
}

func (s *Server) handleMarketCreatorStations(w http.ResponseWriter, r *http.Request) {
	timer := newStationTimer()
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	qStart := time.Now()
	items, err := s.d.Discussions.ListByCreator(
		r.Context(),
		user.ID,
		r.PathValue("id"),
		strings.TrimSpace(r.URL.Query().Get("q")),
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareMarketDiscussions(r, items, timer)
	writeJSON(w, items)
	s.logStationTiming("market.creator", len(items), timer)
}

func (s *Server) handleMarketCreatorFollow(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	profile, err := s.d.Discussions.FollowCreator(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if profile == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, profile)
}

func (s *Server) handleMarketCreatorUnfollow(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	profile, err := s.d.Discussions.UnfollowCreator(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if profile == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, profile)
}

func (s *Server) handleMarketCreatorFollowing(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	items, err := s.d.Discussions.ListFollowing(
		r.Context(),
		user.ID,
		atoiDefault(r.URL.Query().Get("limit"), 0),
		atoiDefault(r.URL.Query().Get("offset"), 0),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
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
	s.applyDiscussionJobStatus(r, updated, true)
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
	// Validate the target language before reserving points so a bad request
	// never touches the ledger.
	if language := normalizeTranslationLanguage(req.Language); language != "" && !strings.EqualFold(language, d.Language) {
		translation, err := s.d.Discussions.TranslationFor(r.Context(), id, language)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if translation == nil {
			http.Error(w, "no translation exists for this language", http.StatusNotFound)
			return
		}
		if translation.Status != DiscussionTranslationReady {
			http.Error(w, "translation is not ready", http.StatusConflict)
			return
		}
		if prompt == "" {
			prompt = translationCoverPrompt(translation, d)
		}
	}
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

func (s *Server) prepareMarketDiscussions(r *http.Request, items []Discussion, timer *stationTimer) {
	viewer := s.requestUser(r).ID
	s.prepareDiscussionListRows(r, items, timer)
	t0 := time.Now()
	s.attachVisibleAlbumSummaries(r.Context(), viewer, items)
	timer.mark("visibleAlbums", t0)
	if !needsCreatorProfileAttach(items) {
		return
	}
	cpStart := time.Now()
	if err := s.d.Discussions.AttachCreatorProfiles(r.Context(), viewer, items); err != nil {
		s.logger().Warn("creator profile attach failed", "err", err)
	}
	timer.mark("creatorProfiles", cpStart)
}

// stationTimer accumulates per-phase durations for station-list endpoints so a
// slow load can be diagnosed. Durations are dumped to stdout (and the structured
// logger) by logStationTiming.
type stationTimer struct {
	start time.Time
	steps []stationStep
}

type stationStep struct {
	name string
	d    time.Duration
}

func newStationTimer() *stationTimer { return &stationTimer{start: time.Now()} }

// mark records the duration of a phase that began at `since`.
func (t *stationTimer) mark(name string, since time.Time) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, stationStep{name: name, d: time.Since(since)})
}

// add records a pre-measured phase duration (e.g. a per-item step accumulated
// across a loop).
func (t *stationTimer) add(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, stationStep{name: name, d: d})
}

// logStationTiming writes a one-line timing breakdown to stdout and the
// structured logger so slow station loads can be debugged.
func (s *Server) logStationTiming(op string, count int, t *stationTimer) {
	if t == nil {
		return
	}
	total := time.Since(t.start)
	var b strings.Builder
	attrs := []any{"op", op, "count", count, "total_ms", durMS(total)}
	for _, st := range t.steps {
		fmt.Fprintf(&b, " %s=%.1fms", st.name, durMS(st.d))
		attrs = append(attrs, st.name+"_ms", durMS(st.d))
	}
	fmt.Fprintf(os.Stdout, "[station-timing] op=%s count=%d total=%.1fms%s\n",
		op, count, durMS(total), b.String())
	s.logger().Info("station timing", attrs...)
}

func durMS(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func needsCreatorProfileAttach(items []Discussion) bool {
	for i := range items {
		if strings.TrimSpace(items[i].OwnerUserID) != "" && items[i].Creator == nil {
			return true
		}
	}
	return false
}

func (s *Server) rememberCreatorProfile(ctx context.Context, user requestUser) {
	if s.d.Discussions == nil {
		return
	}
	if err := s.d.Discussions.UpsertCreatorProfile(ctx, CreatorProfile{
		ID:          user.ID,
		DisplayName: userDisplayName(user),
		AvatarURL:   user.AvatarURL,
	}); err != nil {
		s.logger().Warn("creator profile upsert failed", "user", user.ID, "err", err)
	}
}

func userDisplayName(user requestUser) string {
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}
	if email := strings.TrimSpace(user.Email); email != "" {
		if before, _, ok := strings.Cut(email, "@"); ok && strings.TrimSpace(before) != "" {
			return strings.TrimSpace(before)
		}
		return email
	}
	return "Creator"
}

func (s *Server) refreshDiscussionCoverURL(ctx context.Context, d *Discussion) {
	if d == nil {
		return
	}
	s.refreshCoverArtURL(ctx, d.ID, &d.Cover)
}

// refreshCoverArtURL re-signs a cover's download URL from its durable image
// key; gradient or key-less covers are left untouched.
func (s *Server) refreshCoverArtURL(ctx context.Context, discussionID string, cover *DiscussionCover) {
	if cover == nil || strings.TrimSpace(cover.ImageKey) == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return
	}
	url, err := s.d.Uploader.DownloadURL(ctx, cover.ImageKey, stationCoverURLTTL)
	if err != nil {
		s.logger().Warn("cover download url failed", "discussion", discussionID, "err", err)
		return
	}
	cover.ImageURL = url
}

// refreshTranslationMetaCoverURLs re-signs the per-language cover carried on
// each translation meta so clients can render language cover thumbnails.
func (s *Server) refreshTranslationMetaCoverURLs(ctx context.Context, d *Discussion) {
	if d == nil {
		return
	}
	for i := range d.Translations {
		s.refreshCoverArtURL(ctx, d.ID, d.Translations[i].Cover)
	}
}

// refreshDiscussionLineAudioURLs re-signs the playback URL of every voice-message
// line from its durable AudioKey, so replay keeps working after the URL captured
// at send time expires. Lines without an AudioKey are left untouched.
func (s *Server) refreshDiscussionLineAudioURLs(ctx context.Context, d *Discussion) {
	if d == nil || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return
	}
	for i := range d.Lines {
		key := strings.TrimSpace(d.Lines[i].AudioKey)
		if key == "" {
			continue
		}
		url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour)
		if err != nil {
			s.logger().Warn("voice message download url failed", "discussion", d.ID, "err", err)
			continue
		}
		d.Lines[i].AudioURL = url
	}
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
	key := s.d.Uploader.Key(fmt.Sprintf(
		"covers/%s/%s-%s%s",
		safeKeySegment(userID),
		safeKeySegment(discID),
		newJobID(),
		filepath.Ext(tmpPath),
	))
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

// translationCoverPrompt seeds the AI cover prompt from the translated title
// so the generated art (including any typography) matches the target language,
// falling back to the source title when the bundle carries none.
func translationCoverPrompt(t *DiscussionTranslation, d *Discussion) string {
	title := strings.TrimSpace(t.Bundle.Title)
	if title == "" {
		title = d.DisplayTitle()
	}
	return "Square podcast cover artwork for " + title
}

func stationCoverGenerationPrompt(subject string) string {
	subject = strings.TrimSpace(subject)
	return fmt.Sprintf(`Create a square podcast cover image for this discussion:
%q

Design it as simple, flat podcast cover artwork: minimal visual elements, clean geometric layout, restrained color palette, crisp edges, and little to no shadows. Avoid busy scenes, realistic lighting, 3D effects, heavy textures, clutter, and detailed illustrations. Use clean modern typography only when it improves the cover. Do not write an essay, markdown, explanation, or article. Return only the generated cover image.`, subject)
}
