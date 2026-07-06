package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/planner"
)

// atoiDefault parses s as an int, returning def when s is empty or invalid.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func boolDefault(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return def
	}
}

// discussionCreateRequest creates an empty placeholder discussion so the client
// gets an id up front, then streams the plan into it via
// /api/discussions/{id}/plan/stream.
//
// The client posts the raw JSONSchemaForm values for the new-discussion form
// (see newDiscussionPrecheckForm) verbatim under Form, plus ReferenceDiscussionID
// which is contextual rather than a form field. The server owns every form key,
// so adding a field to the precheck schema only requires reading it here — the
// client renders and submits the form without knowing any key.
type discussionCreateRequest struct {
	Form                  newDiscussionForm `json:"form"`
	ReferenceDiscussionID string            `json:"reference_discussion_id,omitempty"`
}

// newDiscussionForm mirrors the structure produced by newDiscussionPrecheckForm:
// a "prompt" group, an optional "reference" group, and a "settings" group.
// Unknown keys are ignored, and any key the client omits decodes to its zero
// value and is defaulted below.
type newDiscussionForm struct {
	Prompt struct {
		Topic string `json:"topic"`
	} `json:"prompt"`
	// Attachments are the user-uploaded reference files chosen in the form
	// (Notion pages, images, documents). They are folded into the initial
	// planning turn so the agent can ground the plan on them — mirroring the
	// follow-up message path's attachment handling.
	Attachments []planner.Attachment `json:"attachments,omitempty"`
	// Reference is the optional parent discussion selected in the form. It is
	// equivalent to the top-level ReferenceDiscussionID (which is still accepted
	// for the contextual "plan from an existing podcast" entry point), but lets
	// the parent be chosen as a first-class form field via the discussion picker.
	Reference struct {
		DiscussionID string `json:"discussion_id"`
	} `json:"reference"`
	Settings struct {
		Type        string `json:"type"`
		Template    string `json:"template"`
		Discussants int    `json:"discussants"`
		Language    string `json:"language"`
		// GenerateCover, when true, kicks off background AI cover-art generation
		// for the new discussion. The placeholder is returned immediately; the
		// cover is filled in asynchronously and picked up the next time the
		// discussion is fetched (e.g. when the player opens).
		GenerateCover bool `json:"generate_cover"`
	} `json:"settings"`
}

type discussionImproveRequest struct {
	Instruction string               `json:"instruction"`
	Attachments []planner.Attachment `json:"attachments,omitempty"`
}

// discussionRenameRequest is the body of PATCH /api/discussions/{id}.
type discussionRenameRequest struct {
	Title string `json:"title"`
}

// discussionAddSourcesRequest carries links the user added in the sources sheet
// so the planner can re-research them and update the plan.
type discussionAddSourcesRequest struct {
	URLs []string `json:"urls"`
}

type discussionSourceSearchRequest struct {
	Query string `json:"query"`
}

// discussionSpeakerModelRequest changes the LLM model assigned to one speaker
// (host, discussant, audiobook narrator, or audiobook speaker, matched by name)
// in a discussion's plan.
type discussionSpeakerModelRequest struct {
	Speaker string `json:"speaker"`
	Model   string `json:"model"`
}

// discussionSpeakerVoiceRequest changes the TTS voice assigned to one speaker
// (host, discussant, audiobook narrator, or audiobook speaker, matched by name)
// in a discussion's plan. An empty voice clears the override back to automatic
// assignment.
type discussionSpeakerVoiceRequest struct {
	Speaker string `json:"speaker"`
	Voice   string `json:"voice"`
}

type discussionSourceSearchResponse struct {
	Sources []config.Source `json:"sources"`
}

var errDiscussionReferenceNotReady = errors.New("reference discussion is not ready")

const (
	addSourcesBackgroundTimeout     = 5 * time.Minute
	discussionStreamRecoveryTimeout = 10 * time.Minute
)

type discussionGenerateRequest struct {
	VideoConfig videoConfigJSON `json:"videoConfig"`
	Language    string          `json:"language"`
	// Chapters is the audiobook batch selection: 1-based indices into the
	// plan's full chapter list, at most audioBookMaxBatchChapters per run.
	// Empty defaults to the first pending chapters. Rejected (400) for
	// non-audiobook scripts.
	Chapters []int `json:"chapters,omitempty"`
}

func (s *Server) handleDiscussionList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	limit := atoiDefault(r.URL.Query().Get("limit"), 0)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	visibility := DiscussionVisibility(strings.TrimSpace(r.URL.Query().Get("visibility")))
	if visibility != "" && visibility != DiscussionPrivate && visibility != DiscussionPublic {
		http.Error(w, "invalid visibility", http.StatusBadRequest)
		return
	}
	contentType := strings.TrimSpace(r.URL.Query().Get("type"))
	if contentType != "" && contentType != config.ContentTypeDiscussion && contentType != config.ContentTypeAudioBook {
		http.Error(w, "invalid type", http.StatusBadRequest)
		return
	}
	timer := newStationTimer()
	var items []Discussion
	var err error
	qStart := time.Now()
	if query != "" && (visibility != "" || contentType != "") {
		items, err = s.d.Discussions.SearchByFilters(r.Context(), user.ID, query, visibility, contentType, limit, offset)
	} else if query != "" {
		items, err = s.d.Discussions.Search(r.Context(), user.ID, query, limit, offset)
	} else if visibility != "" || contentType != "" {
		items, err = s.d.Discussions.ListByFilters(r.Context(), user.ID, visibility, contentType, limit, offset)
	} else {
		items, err = s.d.Discussions.List(r.Context(), user.ID, limit, offset)
	}
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareDiscussionListRows(r, items, timer)
	writeJSON(w, items)
	s.logStationTiming("discussions.list", len(items), timer)
}

func (s *Server) handleDiscussionGet(w http.ResponseWriter, r *http.Request) {
	timer := newStationTimer()
	editLimit := atoiDefault(r.URL.Query().Get("edit_limit"), 0)
	editBefore, _ := strconv.ParseInt(r.URL.Query().Get("edit_before"), 10, 64)
	includeEditTurns := boolDefault(r.URL.Query().Get("include_edit_turns"), true)
	if editLimit > 0 {
		user := s.requestUser(r)
		d, err := s.d.Discussions.GetWithEditTurnPageTimed(r.Context(), user.ID, r.PathValue("id"), editLimit, editBefore, timer)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.NotFound(w, r)
			return
		}
		s.prepareDiscussionDetail(r, d, false, timer)
		s.logDiscussionSummaryReturn("discussions.get", d)
		writeStart := time.Now()
		writeJSON(w, d)
		timer.mark("write_json", writeStart)
		s.logStationTiming("discussions.get", len(d.Lines), timer)
		return
	}
	d := s.getOwnedDiscussionWithOptionsTimed(w, r, includeEditTurns, timer)
	if d == nil {
		return
	}
	s.prepareDiscussionDetail(r, d, true, timer)
	s.logDiscussionSummaryReturn("discussions.get", d)
	writeStart := time.Now()
	writeJSON(w, d)
	timer.mark("write_json", writeStart)
	s.logStationTiming("discussions.get", len(d.Lines), timer)
}

// handleDiscussionRename serves PATCH /api/discussions/{id}: renames an owned
// podcast without changing its embedded generated plan.
func (s *Server) handleDiscussionRename(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req discussionRenameRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		http.Error(w, "discussion title is required", http.StatusBadRequest)
		return
	}
	d, err := s.d.Discussions.Rename(r.Context(), user.ID, r.PathValue("id"), req.Title)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	timer := newStationTimer()
	s.prepareDiscussionDetail(r, d, true, timer)
	writeJSON(w, d)
}

// handleDiscussionCreate inserts an empty placeholder discussion (status
// "planning") and returns it immediately so the client can navigate to the plan
// page and stream the plan into it via /api/discussions/{id}/plan/stream. This
// decouples discussion creation from the multi-minute planning run: even if the
// stream drops, the discussion is already saved and recoverable in the library.
func (s *Server) handleDiscussionCreate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req discussionCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	settings := req.Form.Settings
	topic := strings.TrimSpace(req.Form.Prompt.Topic)
	if topic == "" {
		http.Error(w, "topic is required", http.StatusBadRequest)
		return
	}
	contentType := strings.TrimSpace(settings.Type)
	if contentType == "" {
		contentType = config.ContentTypeDiscussion
	}
	if contentType != config.ContentTypeDiscussion && contentType != config.ContentTypeAudioBook {
		http.Error(w, "only discussion and audio-book creation are supported", http.StatusBadRequest)
		return
	}
	language := strings.TrimSpace(settings.Language)
	// Prefer the parent chosen in the form (reference.discussion_id); fall back to
	// the contextual top-level reference_discussion_id.
	refID := strings.TrimSpace(req.Form.Reference.DiscussionID)
	if refID == "" {
		refID = strings.TrimSpace(req.ReferenceDiscussionID)
	}
	var reference *planner.PodcastReference
	if refID != "" {
		ref, err := s.discussionReferenceForPlanning(r.Context(), user.ID, refID)
		if errors.Is(err, errDiscussionReferenceNotReady) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ref == nil {
			http.Error(w, "reference discussion is not visible", http.StatusNotFound)
			return
		}
		reference = ref
	}
	template := strings.TrimSpace(settings.Template)
	if _, ok := planner.TemplateByID(contentType, template); !ok {
		template = planner.DefaultTemplateID
	}
	d, err := s.d.Discussions.CreatePlaceholder(r.Context(), user.ID, topic, language, template)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if reference != nil {
		updated, err := s.d.Discussions.SetReference(r.Context(), user.ID, d.ID, reference.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if updated != nil {
			d = updated
		}
		// Follow-ups are auto-bundled into their parent chain's album so the
		// home list groups them. Only possible when the parent is owned by the
		// requester (albums are per-owner); failures are non-fatal.
		if parent, err := s.d.Discussions.Get(r.Context(), user.ID, reference.ID); err == nil && parent != nil {
			if root, err := s.albumChainRoot(r.Context(), user.ID, parent); err == nil && root != nil {
				if err := s.autoBundleFollowUp(r.Context(), user.ID, root, d.ID, nil); err != nil {
					s.logger().Warn("follow-up album bundling failed", "parent", reference.ID, "child", d.ID, "err", err)
				} else if refreshed, err := s.d.Discussions.Get(r.Context(), user.ID, d.ID); err == nil && refreshed != nil {
					d = refreshed
				}
			}
		}
	}
	if settings.GenerateCover {
		s.startBackgroundCoverGeneration(user.ID, d.ID, "", topic)
	}
	if s.d.Planning != nil {
		req.Form.Attachments = s.sanitizedAttachments(user.ID, req.Form.Attachments)
		plan := planner.PlanRequest{
			Type:        contentType,
			Topic:       topic,
			Language:    language,
			Discussants: settings.Discussants,
			Template:    template,
			Research:    true,
			Reference:   reference,
			Attachments: req.Form.Attachments,
		}
		conv, err := s.d.Planning.EnsureConversation(r.Context(), user.ID, d.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var refs []planner.PodcastReference
		if reference != nil {
			refs = []planner.PodcastReference{*reference}
		}
		if err := s.d.Planning.AppendTurn(r.Context(), conv.ID, planningTurnInput{
			Role:        "user",
			Text:        planner.ConversationInitialText(plan),
			References:  refs,
			Attachments: req.Form.Attachments,
			OpID:        "initial:" + d.ID,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, d)
}

func (s *Server) handleDiscussionParentPodcastList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	limit := atoiDefault(r.URL.Query().Get("limit"), 0)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	timer := newStationTimer()
	qStart := time.Now()
	items, err := s.d.Discussions.ListParentPodcasts(r.Context(), user.ID, query, limit, offset)
	timer.mark("query", qStart)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.prepareDiscussionListRows(r, items, timer)
	writeJSON(w, items)
	s.logStationTiming("discussions.parent_podcasts.list", len(items), timer)
}

func (s *Server) handleDiscussionParentPodcastGet(w http.ResponseWriter, r *http.Request) {
	ref, err := s.discussionReferenceForPlanning(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if errors.Is(err, errDiscussionReferenceNotReady) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ref == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, ref)
}

// startBackgroundCoverGeneration reserves points and spawns a goroutine that
// generates AI cover art for a discussion, persisting it when ready. It is
// fire-and-forget: the caller has already returned the discussion, so failures
// (including insufficient points or storage being disabled) are logged and the
// reservation refunded rather than surfaced to the client.
func (s *Server) startBackgroundCoverGeneration(userID, discID, prompt, topic string) {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		s.logger().Warn("skipping background cover generation: storage disabled", "discussion", discID)
		return
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Square podcast cover artwork for " + strings.TrimSpace(topic)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), coverGenerationBackgroundTimeout)
		defer cancel()
		reserved, reserveLedgerID, ok := s.reserveImageGenerationBackground(ctx, userID, discID)
		if !ok {
			return
		}
		cover, err := s.generateStationCover(ctx, userID, discID, prompt)
		if err != nil {
			s.refundImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
			s.logger().Warn("background cover generation failed", "discussion", discID, "err", err)
			return
		}
		if _, err := s.d.Discussions.SetCover(ctx, userID, discID, cover); err != nil {
			s.refundImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
			s.logger().Warn("background cover persist failed", "discussion", discID, "err", err)
			return
		}
		s.settleImageGeneration(ctx, userID, discID, reserved, reserveLedgerID)
	}()
}

func (s *Server) discussionReferenceForPlanning(ctx context.Context, viewer, id string) (*planner.PodcastReference, error) {
	d, err := s.d.Discussions.GetVisible(ctx, viewer, id)
	if err != nil || d == nil {
		return nil, err
	}
	if d.Status != DiscussionReady {
		return nil, errDiscussionReferenceNotReady
	}
	title := strings.TrimSpace(d.Title)
	if title == "" && d.Script != nil {
		title = strings.TrimSpace(d.Script.Title)
	}
	if title == "" {
		title = strings.TrimSpace(d.Topic)
	}
	var sections []string
	if doc, err := s.d.Discussions.SummaryDocumentFor(ctx, d.ID, SummaryDocTypeSummary); err != nil {
		return nil, err
	} else if doc != nil && strings.TrimSpace(doc.Markdown) != "" {
		sections = append(sections, "Summary:\n"+strings.TrimSpace(doc.Markdown))
	}
	if d.Script != nil && strings.TrimSpace(d.Script.Background) != "" {
		sections = append(sections, "Original plan background:\n"+strings.TrimSpace(d.Script.Background))
	}
	if len(d.Lines) > 0 {
		sections = append(sections, "Transcript excerpts:\n"+discussionReferenceTranscript(d.Lines, 9000))
	}
	return &planner.PodcastReference{
		ID:      d.ID,
		Title:   title,
		Topic:   d.Topic,
		Context: strings.Join(sections, "\n\n"),
	}, nil
}

func discussionReferenceTranscript(lines []DiscussionLine, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 9000
	}
	var sb strings.Builder
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" {
			continue
		}
		speaker := strings.TrimSpace(line.Speaker)
		if speaker == "" {
			speaker = strings.TrimSpace(line.Role)
		}
		if speaker == "" {
			speaker = "Speaker"
		}
		next := fmt.Sprintf("%s: %s\n", speaker, text)
		if sb.Len() > 0 && sb.Len()+len(next) > maxChars {
			sb.WriteString("...\n")
			break
		}
		sb.WriteString(next)
	}
	return strings.TrimSpace(sb.String())
}

// handleDiscussionCoverSet persists a cover (gradient, uploaded image, or a
// previously generated AI image) on an owned discussion without changing its
// visibility, so any discussion can carry cover art.
func (s *Server) handleDiscussionCoverSet(w http.ResponseWriter, r *http.Request) {
	var req marketCoverSetRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	updated, err := s.d.Discussions.SetCover(r.Context(), s.requestUser(r).ID, r.PathValue("id"), req.Cover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

// handleUpdateSpeakerModel changes the LLM model for one speaker, matched by
// name, in an owned discussion's plan. The updated model is picked up at
// generation time.
func (s *Server) handleUpdateSpeakerModel(w http.ResponseWriter, r *http.Request) {
	var req discussionSpeakerModelRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	speaker := strings.TrimSpace(req.Speaker)
	model := strings.TrimSpace(req.Model)
	if speaker == "" || model == "" {
		http.Error(w, "speaker and model are required", http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetSpeakerModel(r.Context(), s.requestUser(r).ID, r.PathValue("id"), speaker, model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

// handleUpdateSpeakerVoice changes the TTS voice override for one speaker,
// matched by name, in an owned discussion's plan. An empty voice clears the
// override. The voice is picked up at generation time.
func (s *Server) handleUpdateSpeakerVoice(w http.ResponseWriter, r *http.Request) {
	var req discussionSpeakerVoiceRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	speaker := strings.TrimSpace(req.Speaker)
	if speaker == "" {
		http.Error(w, "speaker is required", http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetSpeakerVoice(r.Context(), s.requestUser(r).ID, r.PathValue("id"), speaker, req.Voice)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

func (s *Server) handleDiscussionCreateFromPlan(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Discussions.CreateFromVisiblePlan(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "plan id is required") || strings.Contains(err.Error(), "source plan is not available") {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleDiscussionPlan(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Reserve before the chargeable planner call; refund if it fails.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, "")
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), user.ID, req.Topic, resp)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reconcile to actual usage against the now-created discussion so the points
	// are never orphaned from the podcast total.
	s.settlePlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(r.Context(), d.ID); err == nil {
		d.PointsCharged = total
	}
	s.notifyPlanReady(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) handleDiscussionImprove(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	res, err := p.Improve(r.Context(), d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, id, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.settlePlanning(r.Context(), user.ID, id, reserved, reserveLedgerID, meter)
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", instruction)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	// Append the plan snapshot before UpdatePlan reloads, so the returned
	// discussion already carries the new plan card in its edit-turn history.
	if err := s.d.Discussions.AppendPlanTurn(r.Context(), user.ID, id, "Updated plan", resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

// handleDiscussionPlanStream is the streaming twin of handleDiscussionPlan: it
// drafts a brand-new plan while emitting coarse progress steps over SSE, then
// sends the persisted discussion in a final "done" event.
func (s *Server) handleDiscussionPlanStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Reserve before SSE starts so a 402 is delivered as an HTTP status.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, "")
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	p.WithProgress(func(ev planner.ProgressEvent) { _ = sse.send("progress", ev) })
	res, err := p.Generate(r.Context(), req)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	d, err := s.d.Discussions.Create(r.Context(), user.ID, req.Topic, resp)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, "", reserved, reserveLedgerID)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	s.settlePlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(r.Context(), d.ID); err == nil {
		d.PointsCharged = total
	}
	s.notifyPlanReady(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	_ = sse.send("done", d)
}

// handleDiscussionPlanStreamForID drafts the plan for an already-created
// placeholder discussion, emitting progress over SSE and persisting the plan
// into the existing row before sending the final "done" event. This is the
// streaming half of the create-then-plan flow: the client first POSTs
// /api/discussions to get an id, then streams the plan into it here.
func (s *Server) handleDiscussionPlanStreamForID(w http.ResponseWriter, r *http.Request) {
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
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	var req planner.PlanRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Topic) == "" {
		req.Topic = d.Topic
	}
	if d.Script != nil {
		s.sanitizeDiscussionUsage(d)
		sse := newSSEWriter(w)
		_ = sse.comment("ok")
		_ = sse.send("done", d)
		return
	}
	if progress := s.currentPlanProgress(r.Context(), id); progress != nil {
		sse := newSSEWriter(w)
		_ = sse.comment("ok")
		s.streamExistingPlanProgress(r.Context(), user.ID, id, sse, progress, nil)
		return
	}
	run, owned := s.claimDiscussionPlanRun(id)
	if !owned {
		sse := newSSEWriter(w)
		_ = sse.comment("ok")
		s.streamExistingPlanProgress(r.Context(), user.ID, id, sse, nil, run)
		return
	}
	defer func() {
		if run.err == nil {
			s.finishDiscussionPlanRun(id, run, nil)
		}
	}()
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		s.finishDiscussionPlanRun(id, run, errors.New("planning reservation failed"))
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	initialProgress := planner.ProgressEvent{Phase: "thinking", Text: "Researching & planning..."}
	s.recordDiscussionProgress(workCtx, id, "plan", initialProgress)
	_ = sse.send("progress", initialProgress)
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "plan", ev)
		_ = sse.send("progress", ev)
	})
	res, err := p.Generate(workCtx, req)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		s.finishDiscussionPlanRun(id, run, err)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		s.finishDiscussionPlanRun(id, run, err)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		s.finishDiscussionPlanRun(id, run, errors.New("discussion not found"))
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	_ = s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Current plan", resp)
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.notifyPlanReady(workCtx, updated)
	s.sanitizeDiscussionUsage(updated)
	s.finishDiscussionPlanRun(id, run, nil)
	_ = sse.send("done", updated)
}

// handleDiscussionImproveStream is the streaming twin of handleDiscussionImprove:
// it revises the plan while emitting progress steps over SSE, then sends the
// updated discussion in a final "done" event.
func (s *Server) handleDiscussionImproveStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionImproveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, id, "improve", planner.ProgressEvent{Phase: "thinking", Text: "Updating plan..."})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "improve", ev)
		_ = sse.send("progress", ev)
	})
	if err := s.d.Discussions.AppendEditTurn(workCtx, user.ID, id, "user", instruction); err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.Improve(workCtx, d.Script, instruction, pastUserMessages(d.EditTurns), req.Attachments)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	// Plan work succeeded — reconcile the reservation to actual usage now, before
	// the persistence steps, so the charge is recorded even if a later write fails.
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	// Append the plan snapshot before UpdatePlan reloads, so the "done" payload
	// already carries the new plan card in its edit-turn history.
	if err := s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Updated plan", resp); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.sanitizeDiscussionUsage(updated)
	_ = sse.send("done", updated)
}

// handleDiscussionAddSources scrapes the user-added links, merges them into the
// plan's sources, and re-runs the planner so the background reflects the new
// references — the "add a link, save, re-research" flow.
func (s *Server) handleDiscussionAddSources(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionAddSourcesRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	urls := cleanedSourceURLs(req.URLs)
	if len(urls) == 0 {
		http.Error(w, "at least one url is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	// Reserve BEFORE launching the background re-research, since the handler
	// returns immediately and can't reject afterwards. The goroutine settles to
	// actual usage on success, or refunds on failure.
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	prev := *d.Script
	prev.Sources = append([]config.Source(nil), d.Sources...)
	urls = append([]string(nil), urls...)
	// Record the user's action up front so the chat history reflects it even if
	// the background re-research later fails.
	_ = s.d.Discussions.AppendEditTurn(r.Context(), user.ID, id, "user", addSourcesTurnText(urls))
	go s.updateDiscussionWithAddedSources(user.ID, id, prev, urls, p, meter, reserved, reserveLedgerID)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

// handleDiscussionAddSourcesStream is the streaming source-update path used by
// the native client. It mirrors the edit stream contract: progress events while
// links are read and the plan is rewritten, then a terminal done/error event so
// the UI never waits on blind polling.
func (s *Server) handleDiscussionAddSourcesStream(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionAddSourcesRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	urls := cleanedSourceURLs(req.URLs)
	if len(urls) == 0 {
		http.Error(w, "at least one url is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, id)
	if !ok {
		return
	}
	meter := &usageAccumulator{}
	p.WithUsageRecorder(meter.record)
	prev := *d.Script
	prev.Sources = append([]config.Source(nil), d.Sources...)

	workCtx, cancel := context.WithTimeout(context.Background(), discussionStreamRecoveryTimeout)
	defer cancel()
	sse := newSSEWriter(w)
	_ = sse.comment("ok")
	s.recordDiscussionProgress(workCtx, id, "sources", planner.ProgressEvent{Phase: "read", Text: "Reading added sources..."})
	p.WithProgress(func(ev planner.ProgressEvent) {
		s.recordDiscussionProgress(workCtx, id, "sources", ev)
		_ = sse.send("progress", ev)
	})

	if err := s.d.Discussions.AppendEditTurn(workCtx, user.ID, id, "user", addSourcesTurnText(urls)); err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	res, err := p.AddSources(workCtx, &prev, urls)
	if err != nil {
		s.refundPlanning(workCtx, user.ID, id, reserved, reserveLedgerID)
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	s.settlePlanning(workCtx, user.ID, id, reserved, reserveLedgerID, meter)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	if err := s.d.Discussions.AppendPlanTurn(workCtx, user.ID, id, "Updated plan with added sources", resp); err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	updated, err := s.d.Discussions.UpdatePlan(workCtx, user.ID, id, resp)
	if err != nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	if updated == nil {
		s.clearDiscussionProgress(workCtx, id)
		_ = sse.send("error", map[string]string{"message": "discussion not found"})
		return
	}
	if total, err := s.pointsCharged(workCtx, id); err == nil {
		updated.PointsCharged = total
	}
	s.clearDiscussionProgress(workCtx, id)
	s.sanitizeDiscussionUsage(updated)
	_ = sse.send("done", updated)
}

// pastUserMessages pulls the text of prior "user" edit turns (oldest first) so
// the planner can revise a plan with the full editing conversation in view, not
// just the latest instruction. Plan-snapshot turns are skipped.
func pastUserMessages(turns []DiscussionEditTurn) []string {
	var out []string
	for _, t := range turns {
		if t.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(t.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// applyDiscussionSummaryMeta attaches the content-free summary descriptor to a
// discussion on the detail path. The Markdown body is intentionally never loaded
// here — clients fetch it from handleDiscussionSummary when the summary view
// mounts. For a ready owner discussion without a summary row, the descriptor
// advertises that manual generation can be started.
func (s *Server) applyDiscussionSummaryMeta(ctx context.Context, d *Discussion) {
	if d == nil || s.d.Discussions == nil {
		return
	}
	if discussionIsAudioBook(d) {
		d.Summary = nil
		return
	}
	if !d.summaryMetaLoaded {
		meta, err := s.d.Discussions.SummaryMetaFor(ctx, d.ID, SummaryDocTypeSummary)
		if err != nil {
			s.logger().Warn("summary meta lookup failed", "discussion", d.ID, "err", err)
			return
		}
		d.Summary = meta
	}
	finalizeDiscussionSummaryMeta(d)
}

// applyDiscussionMindmapMeta attaches the content-free mindmap descriptor to a
// discussion on the detail path. Mindmaps exist only for discussion-type
// podcasts; for a ready owner discussion without a mindmap row, the descriptor
// advertises that manual generation can be started.
func (s *Server) applyDiscussionMindmapMeta(ctx context.Context, d *Discussion) {
	if d == nil || s.d.Discussions == nil {
		return
	}
	if !discussionIsDiscussion(d) {
		d.Mindmap = nil
		return
	}
	meta, err := s.d.Discussions.SummaryMetaFor(ctx, d.ID, SummaryDocTypeMindmap)
	if err != nil {
		s.logger().Warn("mindmap meta lookup failed", "discussion", d.ID, "err", err)
		return
	}
	d.Mindmap = meta
	if d.Mindmap == nil && d.Status == DiscussionReady {
		d.Mindmap = &SummaryMeta{
			DocType:    SummaryDocTypeMindmap,
			Generation: d.IsOwner,
		}
	}
	if d.Mindmap != nil && !d.Mindmap.Available && !d.Mindmap.Pending && d.Status == DiscussionReady {
		d.Mindmap.Generation = d.IsOwner
	}
}

func finalizeDiscussionSummaryMeta(d *Discussion) {
	if d == nil {
		return
	}
	if d.Summary == nil && d.Status == DiscussionReady {
		d.Summary = &SummaryMeta{
			DocType:    SummaryDocTypeSummary,
			Generation: d.IsOwner,
		}
	}
	if d.Summary != nil && !d.Summary.Available && !d.Summary.Pending && d.Status == DiscussionReady {
		d.Summary.Generation = d.IsOwner
	}
}

// handleDiscussionSummaryGenerate lets the discussion owner manually start or
// retry summary generation after the podcast is ready. It returns the refreshed
// discussion detail so clients can immediately render the pending menu state.
func (s *Server) handleDiscussionSummaryGenerate(w http.ResponseWriter, r *http.Request) {
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
	if d.Status != DiscussionReady {
		http.Error(w, "discussion is not ready", http.StatusConflict)
		return
	}
	if discussionIsAudioBook(d) {
		http.Error(w, "summary generation is not available for audio books", http.StatusConflict)
		return
	}
	input := SummaryGenerationInputFromDiscussion(d)
	if _, err := StartSummaryGeneration(r.Context(), SummaryGenerationDeps{
		Env:         s.d.Env,
		Bus:         s.d.Bus,
		Discussions: s.d.Discussions,
		Points:      s.d.Points,
		APNS:        s.apns,
		Log:         s.logger(),
		MQ:          s.d.MQ,
	}, input); err != nil {
		if errors.Is(err, ErrSummaryNoTranscript) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if updated == nil {
		http.NotFound(w, r)
		return
	}
	s.applyDiscussionSummaryMeta(r.Context(), updated)
	s.applyDiscussionMindmapMeta(r.Context(), updated)
	s.logDiscussionSummaryReturn("discussions.summary.generate", updated)
	writeJSON(w, updated)
}

func discussionIsAudioBook(d *Discussion) bool {
	return d != nil && d.Script != nil && strings.TrimSpace(d.Script.Type) == config.ContentTypeAudioBook
}

func discussionIsDiscussion(d *Discussion) bool {
	return d != nil && d.Script != nil && strings.TrimSpace(d.Script.Type) == config.ContentTypeDiscussion
}

// handleDiscussionSummary serves the generated summary document's Markdown body
// for a discussion the requester can see. This is the separate content endpoint
// the client calls only when the summary view mounts — the podcast detail never
// carries the body. Returns 404 when no summary exists yet.
func (s *Server) handleDiscussionSummary(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	docType := strings.TrimSpace(r.URL.Query().Get("doc_type"))
	// Visibility gate: a viewer may read the summary of any discussion they can
	// see (their own, or a public one). GetVisible returns nil when neither holds.
	visible, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if visible == nil {
		http.NotFound(w, r)
		return
	}
	doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), id, docType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if doc == nil {
		http.NotFound(w, r)
		return
	}
	// Embed a "listen again" link on read so the Markdown download (and the
	// in-app summary view, which renders this body) always link back to the
	// original podcast with the current frontend URL.
	doc.Markdown = s.summaryMarkdownWithLink(id, doc.Markdown)
	if normalizeDocType(docType) == SummaryDocTypeSummary {
		doc.Markdown = s.summaryMarkdownWithMindmapLink(r.Context(), visible, doc.Markdown)
	}
	writeJSON(w, doc)
}

// handleDiscussionSummaryPDF renders the summary document to a PDF (via
// Cloudflare Browser Rendering) and streams it back as an attachment. Same
// visibility gate as handleDiscussionSummary; 404 when no summary exists, 503
// when PDF export isn't configured.
func (s *Server) handleDiscussionSummaryPDF(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	docType := strings.TrimSpace(r.URL.Query().Get("doc_type"))
	visible, err := s.d.Discussions.GetVisible(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if visible == nil {
		http.NotFound(w, r)
		return
	}
	doc, err := s.d.Discussions.SummaryDocumentFor(r.Context(), id, docType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if doc == nil {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(visible.Title)
	if title == "" {
		title = strings.TrimSpace(visible.Topic)
	}

	cacheKey := s.summaryPDFCacheKey(id, doc)

	// Serve a previously-rendered PDF from object storage when available. The
	// Cloudflare render is the expensive step, so we pay it only once per summary
	// version — the key embeds the summary's generated-at, so a regenerated
	// summary renders fresh rather than serving a stale PDF.
	if cacheKey != "" {
		if info, err := s.d.Uploader.Head(r.Context(), cacheKey); err == nil && info.ContentLength > 0 {
			if data, err := s.d.Uploader.Download(r.Context(), cacheKey); err == nil && len(data) > 0 {
				s.writeSummaryPDF(w, title, data)
				return
			}
		}
	}

	pdf, err := summaryPDFFromMarkdown(r.Context(), s.d.Env, title, s.summaryMarkdownWithLink(id, doc.Markdown))
	if errors.Is(err, errCloudflareNotConfigured) {
		http.Error(w, "summary PDF export is not configured", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		s.logger().Error("summary pdf render failed", "discussion", id, "err", err)
		http.Error(w, "failed to render summary PDF", http.StatusBadGateway)
		return
	}

	// Cache for next time — best-effort; a storage failure must not fail the
	// download the user is waiting on.
	if cacheKey != "" {
		if err := s.d.Uploader.UploadBytes(r.Context(), cacheKey, "application/pdf", pdf); err != nil {
			s.logger().Warn("summary pdf cache upload failed", "discussion", id, "err", err)
		}
	}

	s.writeSummaryPDF(w, title, pdf)
}

// summaryPDFCacheKey returns the object-storage key for a summary's rendered PDF,
// or "" when uploads are disabled. The key embeds the summary's generated-at so a
// regenerated summary produces a new key (old PDFs are orphaned, never stale).
func (s *Server) summaryPDFCacheKey(discussionID string, doc *SummaryDocument) string {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() || doc == nil {
		return ""
	}
	docType := strings.TrimSpace(doc.DocType)
	if docType == "" {
		docType = SummaryDocTypeSummary
	}
	var gen int64
	if doc.GeneratedAt != nil {
		gen = doc.GeneratedAt.Unix()
	}
	return s.d.Uploader.Key(fmt.Sprintf("summary-pdf/%s/%s-v%s-%d.pdf",
		discussionID, docType, summaryPDFTemplateVersion, gen))
}

// writeSummaryPDF streams the PDF bytes as a download attachment.
func (s *Server) writeSummaryPDF(w http.ResponseWriter, title string, pdf []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", summaryPDFFilename(title)))
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	_, _ = w.Write(pdf)
}

// summaryPDFFilename derives a safe, human-friendly download filename from the
// discussion title.
func summaryPDFFilename(title string) string {
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
	return name + ".pdf"
}

func (s *Server) applyDiscussionProgress(ctx context.Context, d *Discussion) {
	if d == nil || s.d.Progress == nil {
		return
	}
	d.Progress = s.d.Progress.Get(ctx, d.ID)
}

func (s *Server) applyDiscussionProgresses(ctx context.Context, items []Discussion) {
	if len(items) == 0 || s.d.Progress == nil {
		return
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}
	progress := s.d.Progress.GetMany(ctx, ids)
	for i := range items {
		items[i].Progress = progress[items[i].ID]
	}
}

func (s *Server) recordDiscussionProgress(ctx context.Context, id, operation string, ev planner.ProgressEvent) {
	if s.d.Progress == nil {
		return
	}
	s.d.Progress.Set(ctx, id, DiscussionProgress{
		Active:    true,
		Operation: operation,
		Phase:     ev.Phase,
		Text:      ev.Text,
	})
}

func (s *Server) clearDiscussionProgress(ctx context.Context, id string) {
	if s.d.Progress != nil {
		s.d.Progress.Clear(ctx, id)
	}
}

func (s *Server) currentPlanProgress(ctx context.Context, id string) *DiscussionProgress {
	if s.d.Progress == nil {
		return nil
	}
	progress := s.d.Progress.Get(ctx, id)
	if progress == nil || !progress.Active || progress.Operation != "plan" {
		return nil
	}
	return progress
}

func (s *Server) claimDiscussionPlanRun(id string) (*discussionPlanRun, bool) {
	s.discussionPlanMu.Lock()
	defer s.discussionPlanMu.Unlock()
	if run := s.discussionPlanRuns[id]; run != nil {
		return run, false
	}
	run := &discussionPlanRun{done: make(chan struct{})}
	s.discussionPlanRuns[id] = run
	return run, true
}

func (s *Server) finishDiscussionPlanRun(id string, run *discussionPlanRun, err error) {
	if run == nil {
		return
	}
	s.discussionPlanMu.Lock()
	if s.discussionPlanRuns[id] != run {
		s.discussionPlanMu.Unlock()
		return
	}
	run.err = err
	delete(s.discussionPlanRuns, id)
	s.discussionPlanMu.Unlock()
	close(run.done)
}

func (s *Server) streamExistingPlanProgress(ctx context.Context, userID, id string, sse *sseWriter, progress *DiscussionProgress, run *discussionPlanRun) {
	if progress != nil {
		_ = sse.send("progress", planner.ProgressEvent{Phase: progress.Phase, Text: progress.Text})
	} else {
		_ = sse.send("progress", planner.ProgressEvent{Phase: "thinking", Text: "Researching & planning..."})
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-doneChan(run):
			d, err := s.d.Discussions.Get(context.Background(), userID, id)
			if err != nil {
				_ = sse.send("error", map[string]string{"message": err.Error()})
				return
			}
			if d != nil && d.Script != nil {
				s.sanitizeDiscussionUsage(d)
				_ = sse.send("done", d)
				return
			}
			if run != nil && run.err != nil {
				_ = sse.send("error", map[string]string{"message": run.err.Error()})
				return
			}
			_ = sse.send("error", map[string]string{"message": "planning did not finish"})
			return
		case <-ticker.C:
			d, err := s.d.Discussions.Get(ctx, userID, id)
			if err != nil {
				continue
			}
			if d != nil && d.Script != nil {
				s.sanitizeDiscussionUsage(d)
				_ = sse.send("done", d)
				return
			}
			if progress := s.currentPlanProgress(ctx, id); progress != nil {
				_ = sse.send("progress", planner.ProgressEvent{Phase: progress.Phase, Text: progress.Text})
				continue
			}
			if run == nil {
				_ = sse.send("error", map[string]string{"message": "planning did not finish"})
				return
			}
		}
	}
}

func doneChan(run *discussionPlanRun) <-chan struct{} {
	if run == nil {
		return nil
	}
	return run.done
}

func cleanedSourceURLs(raw []string) []string {
	urls := make([]string, 0, len(raw))
	for _, u := range raw {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	return urls
}

// addSourcesTurnText renders the user-visible chat bubble for an add-sources
// action: a short header plus the links the user added.
func addSourcesTurnText(urls []string) string {
	var sb strings.Builder
	sb.WriteString("Added ")
	sb.WriteString(strconv.Itoa(len(urls)))
	sb.WriteString(" source")
	if len(urls) != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(":")
	for _, u := range urls {
		sb.WriteString("\n")
		sb.WriteString(u)
	}
	return sb.String()
}

func (s *Server) updateDiscussionWithAddedSources(owner, id string, prev config.DebateTopic, urls []string, p *planner.Planner, meter *usageAccumulator, reserved, reserveLedgerID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), addSourcesBackgroundTimeout)
	defer cancel()
	res, err := p.AddSources(ctx, &prev, urls)
	if err != nil {
		// Release the held reservation since no chargeable work landed.
		s.refundPlanning(ctx, owner, id, reserved, reserveLedgerID)
		s.logger().Warn("add sources background update failed", "discussion", id, "err", err)
		return
	}
	// Reconcile the reservation to actual usage now that the async run succeeded.
	s.settlePlanning(ctx, owner, id, reserved, reserveLedgerID, meter)
	resp := planResponse{Script: res.Script, Markdown: res.Markdown, Sources: res.Sources, Researched: res.Researched}
	updated, err := s.d.Discussions.UpdatePlan(ctx, owner, id, resp)
	if err != nil {
		s.logger().Warn("add sources plan update failed", "discussion", id, "err", err)
		return
	}
	if updated == nil {
		s.logger().Warn("add sources plan update target disappeared", "discussion", id)
		return
	}
	if err := s.d.Discussions.AppendPlanTurn(ctx, owner, id, "Updated plan with added sources", resp); err != nil {
		s.logger().Warn("add sources edit turn append failed", "discussion", id, "err", err)
	}
}

// handleDiscussionSearchSources searches Firecrawl for candidate web sources
// without mutating the discussion. The native client adds chosen results to
// its local link list, where the user can swipe-delete before saving.
//
// This hits the paid Firecrawl search API, so it is metered like planning: a
// flat search fee is reserved before the call (402 when the balance is short)
// and charged on success / refunded on failure. Firecrawl cost isn't itemised,
// so the reserved fee is charged in full as the flat actual.
func (s *Server) handleDiscussionSearchSources(w http.ResponseWriter, r *http.Request) {
	d := s.getOwnedDiscussion(w, r)
	if d == nil {
		return
	}
	user := s.requestUser(r)
	var req discussionSourceSearchRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	p, err := planner.New(s.d.Env)
	if err != nil {
		http.Error(w, "planning not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	reserved, reserveLedgerID, ok := s.reservePlanning(w, r, user.ID, d.ID)
	if !ok {
		return
	}
	sources, err := p.SearchSources(r.Context(), query)
	if err != nil {
		s.refundPlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.settleFlatPlanning(r.Context(), user.ID, d.ID, reserved, reserveLedgerID)
	writeJSON(w, discussionSourceSearchResponse{Sources: sources})
}

func (s *Server) handleDiscussionGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	var req discussionGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if lang := strings.TrimSpace(req.Language); lang != "" {
		next := *d.Script
		next.Language = lang
		md, err := next.RenderMarkdown()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sources := d.Sources
		if len(sources) == 0 {
			sources = next.Sources
		}
		updated, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, id, planResponse{
			Script:     &next,
			Markdown:   md,
			Sources:    sources,
			Researched: d.Researched,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if updated == nil || updated.Script == nil {
			http.NotFound(w, r)
			return
		}
		d = updated
	}
	// Audiobooks generate in chapter batches: validate the selection against
	// the chain's progress and submit a derived batch script (chapters sliced,
	// outline renumbered globally, minutes budgeted by the batch). Other
	// content types submit the stored script unchanged.
	genScript := d.Script
	if discussionIsAudioBook(d) {
		batch, status, err := s.prepareAudioBookGeneration(r.Context(), user.ID, d, req.Chapters)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		genScript = batch
	} else if len(req.Chapters) > 0 {
		http.Error(w, "chapter selection is only supported for audiobooks", http.StatusBadRequest)
		return
	}
	// Atomically reserve enough points to cover a full podcast of this duration
	// BEFORE submitting the job, so a run never starts uncharged and two
	// concurrent requests can't overdraw. Reconciled to actual usage at job
	// completion; refunded here if the job fails to start.
	reserved, ok := s.reserveGeneration(w, r, user.ID, id, genScript)
	if !ok {
		return
	}
	jobID, err := s.submitJSONScript(genScript, req.VideoConfig, id)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, id, reserved)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetJob(r.Context(), user.ID, id, jobID)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, id, reserved)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

// discussionAppendLineRequest is the body of POST /api/discussions/{id}/lines.
// audio_key is accepted from the client but validated against the sender before
// persistence; audio_url is intentionally not accepted (the server derives the
// playback URL from the validated key on read).
type discussionAppendLineRequest struct {
	Speaker  string `json:"speaker"`
	Role     string `json:"role"`
	Side     string `json:"side"`
	Text     string `json:"text"`
	StartMS  int64  `json:"start_ms"`
	IsUser   bool   `json:"is_user"`
	AudioKey string `json:"audio_key"`
}

func (s *Server) handleDiscussionAppendLine(w http.ResponseWriter, r *http.Request) {
	var req discussionAppendLineRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	user := s.requestUser(r)
	line := DiscussionLine{
		Speaker:  req.Speaker,
		Role:     req.Role,
		Side:     req.Side,
		Text:     req.Text,
		StartMS:  req.StartMS,
		IsUser:   req.IsUser,
		AudioKey: s.validatedAudioKey(user.ID, req.AudioKey),
	}
	token := strings.TrimSpace(r.Header.Get("X-Share-Token"))
	if err := s.d.Discussions.AppendLineVisibleWithToken(r.Context(), user.ID, r.PathValue("id"), token, line); err != nil {
		writeDiscussionAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDiscussionAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDiscussionNotVisible):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errDiscussionForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleDiscussionDelete(w http.ResponseWriter, r *http.Request) {
	ok, err := s.d.Discussions.Delete(r.Context(), s.requestUser(r).ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getOwnedDiscussion(w http.ResponseWriter, r *http.Request) *Discussion {
	return s.getOwnedDiscussionTimed(w, r, nil)
}

func (s *Server) getOwnedDiscussionTimed(w http.ResponseWriter, r *http.Request, timer *stationTimer) *Discussion {
	return s.getOwnedDiscussionWithOptionsTimed(w, r, true, timer)
}

func (s *Server) getOwnedDiscussionWithOptionsTimed(w http.ResponseWriter, r *http.Request, includeEditTurns bool, timer *stationTimer) *Discussion {
	var d *Discussion
	var err error
	if includeEditTurns {
		d, err = s.d.Discussions.GetTimed(r.Context(), s.requestUser(r).ID, r.PathValue("id"), timer)
	} else {
		d, err = s.d.Discussions.GetWithoutEditTurnsTimed(r.Context(), s.requestUser(r).ID, r.PathValue("id"), timer)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if d == nil {
		http.NotFound(w, r)
		return nil
	}
	return d
}

func (s *Server) prepareDiscussionDetail(r *http.Request, d *Discussion, includeShareURL bool, timer *stationTimer) {
	t0 := time.Now()
	s.applyDiscussionJobStatusTimed(r, d, true, timer)
	if timer != nil {
		timer.mark("job_status", t0)
	}
	t0 = time.Now()
	s.applyDiscussionProgress(r.Context(), d)
	if timer != nil {
		timer.mark("progress", t0)
	}
	t0 = time.Now()
	s.applyDiscussionSummaryMeta(r.Context(), d)
	s.applyDiscussionMindmapMeta(r.Context(), d)
	if timer != nil {
		timer.mark("summary", t0)
	}
	t0 = time.Now()
	s.refreshDiscussionCoverURL(r.Context(), d)
	if timer != nil {
		timer.mark("cover", t0)
	}
	t0 = time.Now()
	s.refreshDiscussionLineAudioURLs(r.Context(), d)
	if timer != nil {
		timer.mark("line_audio_urls", t0)
	}
	t0 = time.Now()
	s.sanitizeDiscussionUsage(d)
	if includeShareURL {
		s.applyDiscussionShareURL(d)
	}
	if timer != nil {
		timer.mark("finalize", t0)
	}
}

func (s *Server) prepareDiscussionListRows(r *http.Request, items []Discussion, timer *stationTimer) {
	var coverDur, summaryDur, usageDur time.Duration
	for i := range items {
		// List rows skip resolving the presigned audio download URL — it is
		// only needed on the detail screen and re-signing it per item is slow.
		t0 := time.Now()
		s.refreshDiscussionCoverURL(r.Context(), &items[i])
		t1 := time.Now()
		s.applyDiscussionSummaryMeta(r.Context(), &items[i])
		t2 := time.Now()
		s.sanitizeDiscussionUsage(&items[i])
		s.applyDiscussionShareURL(&items[i])
		items[i].DownloadURL = ""
		coverDur += t1.Sub(t0)
		summaryDur += t2.Sub(t1)
		usageDur += time.Since(t2)
	}
	timer.add("cover", coverDur)
	timer.add("summary", summaryDur)
	timer.add("usage", usageDur)
	t0 := time.Now()
	s.attachAlbumSummaries(r.Context(), s.requestUser(r).ID, items)
	timer.add("albums", time.Since(t0))
}

func (s *Server) logDiscussionSummaryReturn(route string, d *Discussion) {
	if d == nil {
		return
	}
	if d.Summary == nil {
		s.logger().Info("discussion summary return",
			"route", route,
			"discussion", d.ID,
			"discussion_status", d.Status,
			"is_owner", d.IsOwner,
			"summary", nil)
		return
	}
	s.logger().Info("discussion summary return",
		"route", route,
		"discussion", d.ID,
		"discussion_status", d.Status,
		"is_owner", d.IsOwner,
		"summary_doc_type", d.Summary.DocType,
		"summary_status", d.Summary.Status,
		"summary_available", d.Summary.Available,
		"summary_pending", d.Summary.Pending,
		"summary_generation", d.Summary.Generation)
}

// applyDiscussionJobStatus reconciles a discussion's status against its job and
// settles usage/points. resolveDownloadURL controls whether the (expensive,
// presigned) audio download URL is resolved.
func (s *Server) applyDiscussionJobStatus(r *http.Request, d *Discussion, resolveDownloadURL bool) {
	s.applyDiscussionJobStatusTimed(r, d, resolveDownloadURL, nil)
}

func (s *Server) applyDiscussionJobStatusTimed(r *http.Request, d *Discussion, resolveDownloadURL bool, timer *stationTimer) {
	if d == nil {
		return
	}
	defer d.refreshComputedFields()
	if d.JobID == "" || s.d.Jobs == nil {
		return
	}
	j := d.joinedJob
	if j == nil {
		t0 := time.Now()
		j = s.d.Jobs.GetWithoutLogs(d.JobID)
		if timer != nil {
			timer.mark("job_lookup", t0)
		}
	} else if timer != nil {
		timer.add("job_lookup_joined", 0)
	}
	if j == nil {
		t0 := time.Now()
		j = s.recoverJob(d.JobID)
		if timer != nil {
			timer.mark("job_recover", t0)
		}
		if j == nil {
			if d.Status == DiscussionGenerating {
				d.Status = DiscussionFailed
				t0 = time.Now()
				_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
				if timer != nil {
					timer.mark("job_set_result", t0)
				}
			}
			return
		}
	}
	s.applyDiscussionJobTimed(r, d, j, resolveDownloadURL, timer)
}

func (s *Server) applyDiscussionJob(r *http.Request, d *Discussion, j *Job, resolveDownloadURL bool) {
	s.applyDiscussionJobTimed(r, d, j, resolveDownloadURL, nil)
}

func (s *Server) applyDiscussionJobTimed(r *http.Request, d *Discussion, j *Job, resolveDownloadURL bool, timer *stationTimer) {
	if d == nil || j == nil {
		return
	}
	defer d.refreshComputedFields()
	switch {
	case j.Status == JobDone:
		originalStatus := d.Status
		originalDownloadURL := d.DownloadURL
		d.Status = DiscussionReady
		persistDownloadURL := d.DownloadURL
		if resolveDownloadURL {
			t0 := time.Now()
			if url := s.jobDownloadURL(r.Context(), j); url != "" {
				d.DownloadURL = url
			} else if d.DownloadURL == "" && j.DownloadURL != "" {
				d.DownloadURL = j.DownloadURL
				persistDownloadURL = j.DownloadURL
			}
			if timer != nil {
				timer.mark("job_download_url", t0)
			}
		}
		detail, hasUsageDetail := generationUsageDetail(j, d)
		needsResultPersist := originalStatus != DiscussionReady || (originalDownloadURL == "" && j.DownloadURL != "")
		needsUsagePersist := jobHasBillableUsage(j) && !discussionUsageMatchesDetail(d, detail)
		if needsResultPersist || needsUsagePersist {
			t0 := time.Now()
			if needsUsagePersist {
				_ = s.d.Discussions.SetJobResultAndUsage(r.Context(), d.ID, DiscussionReady, persistDownloadURL, detail)
			} else {
				_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionReady, persistDownloadURL)
			}
			if timer != nil {
				timer.mark("job_persist_discussion", t0)
			}
		}
		if jobHasBillableUsage(j) {
			d.PromptTokens = j.PromptTokens
			d.CompletionTokens = j.CompletionTokens
			d.TotalTokens = j.TotalTokens
			d.LLMCostUSD = j.LLMCostUSD
			d.LLMCostKnown = j.LLMCostKnown
			d.TTSCostUSD = j.TTSCostUSD
			d.MusicCostUSD = j.MusicCostUSD
		}
		// Reconcile the generation reservation against actual usage. This is a
		// lazy fallback (the job-completion path also reconciles); both call the
		// idempotent SettleGeneration so the charge applies exactly once. Use the
		// discussion's persisted usage when a recovered/done job has lost its
		// usage fields.
		if s.pointsEnabled() && d.PointsReserved > 0 {
			if hasUsageDetail {
				t0 := time.Now()
				total, err := s.d.Points.ChargeGenerationKnown(r.Context(), s.d.Env, d.OwnerUserID, d.ID, d.PointsReserved, d.PointsCharged, detail)
				if err != nil {
					s.logger().Warn("generation settle failed", "discussion", d.ID, "err", err)
				} else {
					d.PointsCharged = total
					d.PointsReserved = 0
				}
				if timer != nil {
					timer.mark("points_charge_generation", t0)
				}
			}
		}
	case j.Status == JobError:
		d.Status = DiscussionFailed
		t0 := time.Now()
		_ = s.d.Discussions.SetJobResult(r.Context(), d.ID, DiscussionFailed, d.DownloadURL)
		if timer != nil {
			timer.mark("job_set_result", t0)
		}
	}
}

func generationUsageDetail(j *Job, d *Discussion) (PointsUsageDetail, bool) {
	if jobHasBillableUsage(j) {
		return PointsUsageDetail{
			PromptTokens:     j.PromptTokens,
			CompletionTokens: j.CompletionTokens,
			TotalTokens:      j.TotalTokens,
			LLMCostUSD:       j.LLMCostUSD,
			LLMCostKnown:     j.LLMCostKnown,
			TTSCostUSD:       j.TTSCostUSD,
			MusicCostUSD:     j.MusicCostUSD,
			CostUSD:          j.LLMCostUSD + j.TTSCostUSD + j.MusicCostUSD,
		}, true
	}
	if !discussionHasBillableUsage(d) {
		return PointsUsageDetail{}, false
	}
	return PointsUsageDetail{
		PromptTokens:     d.PromptTokens,
		CompletionTokens: d.CompletionTokens,
		TotalTokens:      d.TotalTokens,
		LLMCostUSD:       d.LLMCostUSD,
		LLMCostKnown:     d.LLMCostKnown,
		TTSCostUSD:       d.TTSCostUSD,
		MusicCostUSD:     d.MusicCostUSD,
		CostUSD:          d.LLMCostUSD + d.TTSCostUSD + d.MusicCostUSD,
	}, true
}

func jobHasBillableUsage(j *Job) bool {
	return j != nil && (j.TotalTokens > 0 || j.LLMCostUSD > 0 || j.TTSCostUSD > 0 || j.MusicCostUSD > 0)
}

func discussionHasBillableUsage(d *Discussion) bool {
	return d != nil && (d.TotalTokens > 0 || d.LLMCostUSD > 0 || d.TTSCostUSD > 0 || d.MusicCostUSD > 0)
}

func discussionUsageMatchesDetail(d *Discussion, detail PointsUsageDetail) bool {
	return d != nil &&
		d.PromptTokens == detail.PromptTokens &&
		d.CompletionTokens == detail.CompletionTokens &&
		d.TotalTokens == detail.TotalTokens &&
		d.LLMCostUSD == detail.LLMCostUSD &&
		d.LLMCostKnown == detail.LLMCostKnown &&
		d.TTSCostUSD == detail.TTSCostUSD &&
		d.MusicCostUSD == detail.MusicCostUSD
}
