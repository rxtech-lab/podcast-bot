package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/planner"
)

// audioBookMaxBatchChapters is the server hard cap on how many chapters a
// single generation run may narrate. Plans can hold many more chapters (see
// planner.audioBookMaxChapters); users generate them in batches of up to this
// many, each batch becoming its own linked podcast.
const audioBookMaxBatchChapters = 5

// Chapter generation states surfaced by GET /api/discussions/{id}/chapters.
const (
	chapterStatusDone       = "done"
	chapterStatusGenerating = "generating"
	chapterStatusPending    = "pending"
)

// audioBookChapterState is one chapter of the root plan annotated with its
// generation progress across the follow-up chain.
type audioBookChapterState struct {
	Index   int    `json:"index"` // 1-based position in the root plan
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Mode    string `json:"mode,omitempty"`
	Status  string `json:"status"`
	// DiscussionID is the discussion that generated (or is generating) this
	// chapter; empty while pending.
	DiscussionID string `json:"discussion_id,omitempty"`
}

// audioBookChaptersResponse is the payload of GET /api/discussions/{id}/chapters.
type audioBookChaptersResponse struct {
	RootID       string                  `json:"root_id"`
	AlbumID      string                  `json:"album_id,omitempty"`
	MaxBatchSize int                     `json:"max_batch_size"`
	Chapters     []audioBookChapterState `json:"chapters"`
}

// audioBookRoot resolves the root discussion of a follow-up chain: the
// discussion itself when it has no reference, otherwise the referenced parent.
// Returns nil when the root isn't visible to the owner.
func (s *Server) audioBookRoot(ctx context.Context, owner string, d *Discussion) (*Discussion, error) {
	if d == nil {
		return nil, nil
	}
	rootID := strings.TrimSpace(d.ReferenceDiscussionID)
	if rootID == "" || rootID == d.ID {
		return d, nil
	}
	return s.d.Discussions.Get(ctx, owner, rootID)
}

// chapterClaim reports which 1-based chapter indices of a root plan (with
// total chapters) discussion d has generated or is generating. A legacy
// single-shot audiobook (no recorded indices) claims every chapter once its
// generation actually started.
func chapterClaim(d *Discussion, total int) []int {
	if d == nil || !discussionIsAudioBook(d) {
		return nil
	}
	if d.Status != DiscussionReady && d.Status != DiscussionGenerating {
		return nil
	}
	indices := d.Script.AudioBookChapterIndices
	if len(indices) == 0 {
		if strings.TrimSpace(d.JobID) == "" {
			return nil
		}
		all := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			all = append(all, i)
		}
		return all
	}
	claimed := make([]int, 0, len(indices))
	for _, idx := range indices {
		if idx >= 1 && idx <= total {
			claimed = append(claimed, idx)
		}
	}
	return claimed
}

// audioBookChapterStates computes per-chapter generation progress for a root
// audiobook by unioning the chapters claimed by the root itself and every
// owned follow-up batch referencing it. "done" wins over "generating"; failed
// runs release their chapters back to "pending". excludeID skips one
// claimant's own contribution — used when re-generating a discussion so its
// previous run doesn't block its own chapters.
func (s *Server) audioBookChapterStates(ctx context.Context, owner string, root *Discussion, excludeID string) ([]audioBookChapterState, error) {
	if root == nil || root.Script == nil {
		return nil, fmt.Errorf("audiobook plan is not available")
	}
	total := len(root.Script.AudioBookChapters)
	states := make([]audioBookChapterState, total)
	for i, ch := range root.Script.AudioBookChapters {
		states[i] = audioBookChapterState{
			Index:   i + 1,
			Title:   ch.Title,
			Summary: ch.Summary,
			Mode:    ch.Mode,
			Status:  chapterStatusPending,
		}
	}
	children, err := s.d.Discussions.ListByReference(ctx, owner, root.ID)
	if err != nil {
		return nil, err
	}
	claimants := append([]Discussion{*root}, children...)
	for i := range claimants {
		d := &claimants[i]
		if excludeID != "" && d.ID == excludeID {
			continue
		}
		status := chapterStatusGenerating
		if d.Status == DiscussionReady {
			status = chapterStatusDone
		}
		for _, idx := range chapterClaim(d, total) {
			st := &states[idx-1]
			// done wins over generating; first claimant wins within a status.
			if st.Status == chapterStatusDone || (st.Status == chapterStatusGenerating && status != chapterStatusDone) {
				continue
			}
			st.Status = status
			st.DiscussionID = d.ID
		}
	}
	return states, nil
}

// validateChapterSelection checks a 1-based batch selection against the
// current chapter states. Every returned error is a client error the HTTP
// layer surfaces as a 400 with the message verbatim.
func validateChapterSelection(states []audioBookChapterState, sel []int) error {
	if len(sel) == 0 {
		return fmt.Errorf("select at least one chapter")
	}
	if len(sel) > audioBookMaxBatchChapters {
		return fmt.Errorf("you can generate at most %d chapters per batch", audioBookMaxBatchChapters)
	}
	seen := make(map[int]bool, len(sel))
	for _, idx := range sel {
		if idx < 1 || idx > len(states) || seen[idx] {
			return fmt.Errorf("invalid chapter selection: %d", idx)
		}
		seen[idx] = true
		switch states[idx-1].Status {
		case chapterStatusDone:
			return fmt.Errorf("chapter %d is already generated", idx)
		case chapterStatusGenerating:
			return fmt.Errorf("chapter %d is currently generating", idx)
		}
	}
	return nil
}

// pendingChapterIndices returns the indices still selectable, in plan order.
func pendingChapterIndices(states []audioBookChapterState) []int {
	var pending []int
	for _, st := range states {
		if st.Status == chapterStatusPending {
			pending = append(pending, st.Index)
		}
	}
	return pending
}

// deriveAudioBookBatchScript slices a root audiobook plan down to the selected
// 1-based chapter indices so a generation run narrates only that batch. The
// outline keeps global chapter numbers (a batch starting at chapter 6 narrates
// "Chapter 6"), minutes are budgeted by the batch size, and `previously` — a
// bounded digest of earlier batches — is prepended to the outline as
// context-only material. This slicing is load-bearing: the pipeline's
// completion gate derives the required final scene from the chapters it is
// handed, so submitting the full plan for a partial batch would demand scenes
// the narrator never reaches.
func deriveAudioBookBatchScript(root *config.DebateTopic, indices []int, previously string, e2e bool) (*config.DebateTopic, error) {
	if root == nil || root.Type != config.ContentTypeAudioBook {
		return nil, fmt.Errorf("chapter batches require an audiobook plan")
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("select at least one chapter")
	}
	sorted := append([]int(nil), indices...)
	sort.Ints(sorted)
	chapters := make([]config.AudioBookChapter, 0, len(sorted))
	for _, idx := range sorted {
		if idx < 1 || idx > len(root.AudioBookChapters) {
			return nil, fmt.Errorf("invalid chapter selection: %d", idx)
		}
		chapters = append(chapters, root.AudioBookChapters[idx-1])
	}

	batch := *root
	batch.AudioBookChapters = chapters
	batch.AudioBookChapterIndices = sorted
	batch.TotalMinutes = len(chapters) * 8
	if batch.TotalMinutes < 15 {
		batch.TotalMinutes = 15
	}
	if e2e {
		batch.TotalMinutes = 1
	}
	if len(sorted) < len(root.AudioBookChapters) {
		title := strings.TrimSpace(root.Title)
		if title == "" {
			title = "Audiobook"
		}
		batch.Title = fmt.Sprintf("%s — %s", title, chapterRangeLabel(sorted))
	}
	surface := planner.RenderAudioBookOutlineIndexed(batch.Background, chapters, sorted, batch.AudioBookHost.Name)
	if prev := strings.TrimSpace(previously); prev != "" {
		// h3, not h2: Surface is re-parsed from script.md where any `## `
		// line ends the section — an h2 prefix here made Surface come back
		// empty and fail audiobook validation.
		surface = "### Previously narrated (context only — do NOT re-narrate)\n\n" + prev + "\n\n" + surface
	}
	batch.Surface = surface
	if err := config.ValidateTopic(&batch); err != nil {
		return nil, fmt.Errorf("derive audiobook batch: %w", err)
	}
	return &batch, nil
}

// audioBookChapterRoot resolves which discussion owns the full chapter plan a
// checklist should display for d. The referenced parent is the root only when
// it is an audiobook and d is one of its chapter batches (marked by recorded
// chapter indices) — a conversational follow-up that happens to be an
// audiobook keeps its own independent plan.
func (s *Server) audioBookChapterRoot(ctx context.Context, owner string, d *Discussion) (*Discussion, error) {
	if d == nil {
		return nil, nil
	}
	if strings.TrimSpace(d.ReferenceDiscussionID) == "" {
		return d, nil
	}
	isBatchChild := d.Script != nil && len(d.Script.AudioBookChapterIndices) > 0
	if !isBatchChild {
		return d, nil
	}
	root, err := s.audioBookRoot(ctx, owner, d)
	if err != nil {
		return nil, err
	}
	if root == nil || !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) == 0 {
		return d, nil
	}
	return root, nil
}

// hasPendingChapters reports whether the chain containing d still has
// ungenerated chapters — gates the "Generate More Chapters" toolbar action.
func (s *Server) hasPendingChapters(r *http.Request, d *Discussion) bool {
	root, err := s.audioBookChapterRoot(r.Context(), d.OwnerUserID, d)
	if err != nil || root == nil || !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) == 0 {
		return false
	}
	states, err := s.audioBookChapterStates(r.Context(), d.OwnerUserID, root, "")
	if err != nil {
		return false
	}
	return len(pendingChapterIndices(states)) > 0
}

// prepareAudioBookGeneration resolves the script POST /generate should submit
// for an audiobook discussion: validates the batch selection against the
// chain's chapter progress, defaults an empty selection to the first pending
// chapters, derives the sliced batch script, and persists the selection on the
// plan. Returns the HTTP status to use when err is non-nil.
func (s *Server) prepareAudioBookGeneration(ctx context.Context, owner string, d *Discussion, sel []int) (*config.DebateTopic, int, error) {
	plan := d.Script
	if plan == nil || len(plan.AudioBookChapters) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("audiobook plan has no chapters")
	}
	// Re-running an existing batch child keeps its recorded slice — selection
	// changes happen by creating a new batch, not by mutating this one.
	if strings.TrimSpace(d.ReferenceDiscussionID) != "" && len(plan.AudioBookChapterIndices) > 0 {
		root, err := s.audioBookRoot(ctx, owner, d)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if root != nil && root.ID != d.ID && discussionIsAudioBook(root) && len(root.Script.AudioBookChapters) > 0 {
			if len(sel) > 0 && !equalIntSets(sel, plan.AudioBookChapterIndices) {
				return nil, http.StatusBadRequest, fmt.Errorf("chapter selection cannot be changed on an existing batch; generate a new batch instead")
			}
			states, err := s.audioBookChapterStates(ctx, owner, root, d.ID)
			if err != nil {
				return nil, http.StatusInternalServerError, err
			}
			if err := validateChapterSelection(states, plan.AudioBookChapterIndices); err != nil {
				return nil, http.StatusBadRequest, err
			}
			// The child's stored script is already the derived batch script
			// (global outline numbering, previously-narrated block, batch
			// minutes) — submit it as-is.
			return plan, 0, nil
		}
	}
	// d is a root (or standalone) audiobook.
	states, err := s.audioBookChapterStates(ctx, owner, d, d.ID)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if len(sel) == 0 {
		pending := pendingChapterIndices(states)
		if len(pending) > audioBookMaxBatchChapters {
			pending = pending[:audioBookMaxBatchChapters]
		}
		sel = pending
	}
	if len(sel) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("all chapters are already generated")
	}
	if err := validateChapterSelection(states, sel); err != nil {
		return nil, http.StatusBadRequest, err
	}
	batch, err := deriveAudioBookBatchScript(plan, sel, "", s.e2eMode())
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	// Persist the selection on the stored plan (full chapter list is kept) so
	// chapter progress survives reloads and other clients.
	full := *plan
	full.AudioBookChapterIndices = batch.AudioBookChapterIndices
	md, err := full.RenderMarkdown()
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if _, err := s.d.Discussions.UpdatePlan(ctx, owner, d.ID, planResponse{
		Script:     &full,
		Markdown:   md,
		Sources:    d.Sources,
		Researched: d.Researched,
	}); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return batch, 0, nil
}

// handleDiscussionChapters serves GET /api/discussions/{id}/chapters: the root
// plan's full chapter list annotated with per-chapter generation progress, for
// the chapter-checklist UI. Works on the root or any batch in its chain.
func (s *Server) handleDiscussionChapters(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	root, err := s.audioBookChapterRoot(r.Context(), user.ID, d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if root == nil || !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) == 0 {
		http.Error(w, "discussion is not an audiobook with chapters", http.StatusBadRequest)
		return
	}
	states, err := s.audioBookChapterStates(r.Context(), user.ID, root, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, audioBookChaptersResponse{
		RootID:       root.ID,
		AlbumID:      root.AlbumID,
		MaxBatchSize: audioBookMaxBatchChapters,
		Chapters:     states,
	})
}

// discussionChaptersGenerateRequest is the body of
// POST /api/discussions/{id}/chapters/generate.
type discussionChaptersGenerateRequest struct {
	Chapters    []int           `json:"chapters"`
	VideoConfig videoConfigJSON `json:"videoConfig"`
	Language    string          `json:"language"`
}

// handleDiscussionChaptersGenerate creates a follow-up batch: a NEW discussion
// linked to the audiobook root that narrates the selected pending chapters. It
// skips conversational planning entirely — the batch script is derived from
// the root plan, seeded with a digest of the newest finished batch for
// continuity, and submitted in the same request.
func (s *Server) handleDiscussionChaptersGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req discussionChaptersGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	d, err := s.d.Discussions.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	root, err := s.audioBookChapterRoot(r.Context(), user.ID, d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if root == nil || !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) == 0 {
		http.Error(w, "discussion is not an audiobook with chapters", http.StatusBadRequest)
		return
	}
	children, err := s.d.Discussions.ListByReference(r.Context(), user.ID, root.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newestReady := newestReadyAudioBook(root, children)
	if newestReady == nil {
		http.Error(w, "generate the first chapters from the plan before creating a follow-up batch", http.StatusConflict)
		return
	}
	states, err := s.audioBookChapterStates(r.Context(), user.ID, root, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sel := req.Chapters
	if len(sel) == 0 {
		pending := pendingChapterIndices(states)
		if len(pending) > audioBookMaxBatchChapters {
			pending = pending[:audioBookMaxBatchChapters]
		}
		sel = pending
	}
	if len(sel) == 0 {
		http.Error(w, "all chapters are already generated", http.StatusBadRequest)
		return
	}
	if err := validateChapterSelection(states, sel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	previously := ""
	if ref, err := s.discussionReferenceForPlanning(r.Context(), user.ID, newestReady.ID); err == nil && ref != nil {
		previously = ref.Context
	}
	rootPlan := *root.Script
	if lang := strings.TrimSpace(req.Language); lang != "" {
		rootPlan.Language = lang
	}
	batch, err := deriveAudioBookBatchScript(&rootPlan, sel, previously, s.e2eMode())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created, err := s.d.Discussions.CreatePlaceholder(r.Context(), user.ID, root.Topic, batch.Language, root.Template)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cleanup := func() { _, _ = s.d.Discussions.Delete(r.Context(), user.ID, created.ID) }
	if _, err := s.d.Discussions.SetReference(r.Context(), user.ID, created.ID, root.ID); err != nil {
		cleanup()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	md, err := batch.RenderMarkdown()
	if err != nil {
		cleanup()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := s.d.Discussions.UpdatePlan(r.Context(), user.ID, created.ID, planResponse{
		Script:     batch,
		Markdown:   md,
		Sources:    root.Sources,
		Researched: root.Researched,
	}); err != nil {
		cleanup()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if root.Cover.Type != "" {
		_, _ = s.d.Discussions.SetCover(r.Context(), user.ID, created.ID, root.Cover)
	}
	if err := s.autoBundleFollowUp(r.Context(), user.ID, root, created.ID, sel); err != nil {
		s.logger().Warn("audiobook batch album bundling failed", "root", root.ID, "batch", created.ID, "err", err)
	}
	reserved, ok := s.reserveGeneration(w, r, user.ID, created.ID, batch)
	if !ok {
		cleanup()
		return
	}
	jobID, err := s.submitJSONScript(batch, req.VideoConfig, created.ID)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, created.ID, reserved)
		cleanup()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.d.Discussions.SetJob(r.Context(), user.ID, created.ID, jobID)
	if err != nil {
		s.refundGeneration(r.Context(), user.ID, created.ID, reserved)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.sanitizeDiscussionUsage(updated)
	writeJSON(w, updated)
}

// newestReadyAudioBook picks the most recently created finished batch in a
// chain (root included) — the continuity source for the next batch.
func newestReadyAudioBook(root *Discussion, children []Discussion) *Discussion {
	var newest *Discussion
	consider := func(d *Discussion) {
		if d == nil || d.Status != DiscussionReady || !discussionIsAudioBook(d) {
			return
		}
		if newest == nil || d.CreatedAt.After(newest.CreatedAt) {
			newest = d
		}
	}
	consider(root)
	for i := range children {
		consider(&children[i])
	}
	return newest
}

func equalIntSets(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]int(nil), a...)
	bs := append([]int(nil), b...)
	sort.Ints(as)
	sort.Ints(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// chapterRangeLabel renders a sorted 1-based selection as a human title
// suffix: "Chapter 4", "Chapters 6-8", or "Chapters 6, 9".
func chapterRangeLabel(sorted []int) string {
	if len(sorted) == 0 {
		return ""
	}
	if len(sorted) == 1 {
		return fmt.Sprintf("Chapter %d", sorted[0])
	}
	contiguous := true
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		return fmt.Sprintf("Chapters %d-%d", sorted[0], sorted[len(sorted)-1])
	}
	parts := make([]string, len(sorted))
	for i, idx := range sorted {
		parts[i] = fmt.Sprint(idx)
	}
	return "Chapters " + strings.Join(parts, ", ")
}
