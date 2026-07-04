package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sirily11/debate-bot/internal/content_creator"
)

// albumBatchPositionBase offsets audiobook chapter batches inside an album so
// they order by first chapter index; members with position 0 (follow-up
// podcasts) fall back to creation-time ordering after them.
const albumBatchPositionBase = 1000

// albumPositionFor computes a member's album_position from its chapter batch
// indices; 0 (created_at ordering) when it isn't a chapter batch.
func albumPositionFor(indices []int) int64 {
	min := 0
	for _, idx := range indices {
		if idx > 0 && (min == 0 || idx < min) {
			min = idx
		}
	}
	if min == 0 {
		return 0
	}
	return albumBatchPositionBase + int64(min)
}

// albumChainRoot follows reference_discussion_id up to the chain's owned root
// (bounded, cycles ignored). Returns d itself when it has no owned parent.
func (s *Server) albumChainRoot(ctx context.Context, owner string, d *Discussion) (*Discussion, error) {
	current := d
	for hop := 0; hop < 5; hop++ {
		refID := strings.TrimSpace(current.ReferenceDiscussionID)
		if refID == "" || refID == current.ID {
			return current, nil
		}
		parent, err := s.d.Discussions.Get(ctx, owner, refID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			// Parent isn't owned by this user (e.g. a public podcast) — the
			// chain root for album purposes is the last owned discussion.
			return current, nil
		}
		current = parent
	}
	return current, nil
}

// ensureAutoAlbum returns the album a root discussion's follow-ups should join,
// creating an auto album (titled and covered from the root) and moving the root
// into it on first use.
func (s *Server) ensureAutoAlbum(ctx context.Context, owner string, root *Discussion) (*Album, error) {
	if strings.TrimSpace(root.AlbumID) != "" {
		album, err := s.d.Discussions.GetAlbum(ctx, owner, root.AlbumID)
		if err != nil {
			return nil, err
		}
		if album != nil {
			return album, nil
		}
	}
	title := strings.TrimSpace(root.Title)
	if title == "" {
		title = strings.TrimSpace(root.Topic)
	}
	album, err := s.d.Discussions.CreateAlbum(ctx, owner, title, albumKindAuto, root.ID, root.Cover)
	if err != nil {
		return nil, err
	}
	rootPos := int64(0)
	if root.Script != nil {
		rootPos = albumPositionFor(root.Script.AudioBookChapterIndices)
	}
	if err := s.d.Discussions.AddDiscussionToAlbum(ctx, owner, album.ID, root.ID, rootPos); err != nil {
		return nil, err
	}
	return album, nil
}

// autoBundleFollowUp groups a newly created follow-up (chapter batch or
// follow-up podcast) into its root's album, creating the auto album on first
// follow-up. chapterIndices orders audiobook batches by chapter; pass nil for
// plain follow-up podcasts (creation-time ordering).
func (s *Server) autoBundleFollowUp(ctx context.Context, owner string, root *Discussion, childID string, chapterIndices []int) error {
	album, err := s.ensureAutoAlbum(ctx, owner, root)
	if err != nil {
		return err
	}
	return s.d.Discussions.AddDiscussionToAlbum(ctx, owner, album.ID, childID, albumPositionFor(chapterIndices))
}

// refreshAlbumCoverURL re-signs an album cover image URL from its durable
// storage key, mirroring refreshDiscussionCoverURL.
func (s *Server) refreshAlbumCoverURL(ctx context.Context, a *Album) {
	if a == nil || strings.TrimSpace(a.Cover.ImageKey) == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return
	}
	url, err := s.d.Uploader.DownloadURL(ctx, a.Cover.ImageKey, stationCoverURLTTL)
	if err != nil {
		s.logger().Warn("album cover download url failed", "album", a.ID, "err", err)
		return
	}
	a.Cover.ImageURL = url
}

func (s *Server) refreshAlbumSummaryCoverURL(ctx context.Context, a *AlbumSummary) {
	if a == nil || strings.TrimSpace(a.Cover.ImageKey) == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return
	}
	url, err := s.d.Uploader.DownloadURL(ctx, a.Cover.ImageKey, stationCoverURLTTL)
	if err != nil {
		s.logger().Warn("album cover download url failed", "album", a.ID, "err", err)
		return
	}
	a.Cover.ImageURL = url
}

// attachAlbumSummaries annotates a page of owned discussion rows with their
// album summaries in one grouped query, so the home list can render album
// groups without extra requests.
func (s *Server) attachAlbumSummaries(ctx context.Context, owner string, items []Discussion) {
	ids := make([]string, 0, len(items))
	for i := range items {
		if items[i].AlbumID != "" && items[i].OwnerUserID == owner {
			ids = append(ids, items[i].AlbumID)
		}
	}
	if len(ids) == 0 {
		return
	}
	summaries, err := s.d.Discussions.AlbumSummariesFor(ctx, owner, ids)
	if err != nil {
		s.logger().Warn("album summaries load failed", "err", err)
		return
	}
	for _, summary := range summaries {
		s.refreshAlbumSummaryCoverURL(ctx, summary)
	}
	for i := range items {
		if items[i].AlbumID != "" {
			items[i].Album = summaries[items[i].AlbumID]
		}
	}
}

// attachVisibleAlbumSummaries annotates visible marketplace rows with album
// summaries. Unlike attachAlbumSummaries, this can attach non-owned albums and
// counts only the episodes the current viewer may see.
func (s *Server) attachVisibleAlbumSummaries(ctx context.Context, viewer string, items []Discussion) {
	ids := make([]string, 0, len(items))
	for i := range items {
		if items[i].AlbumID != "" {
			ids = append(ids, items[i].AlbumID)
		}
	}
	if len(ids) == 0 {
		return
	}
	summaries, err := s.d.Discussions.AlbumSummariesForVisible(ctx, viewer, ids)
	if err != nil {
		s.logger().Warn("visible album summaries load failed", "err", err)
		return
	}
	for _, summary := range summaries {
		s.refreshAlbumSummaryCoverURL(ctx, summary)
	}
	for i := range items {
		if items[i].AlbumID != "" {
			items[i].Album = summaries[items[i].AlbumID]
		}
	}
}

// albumCreateRequest is the body of POST /api/albums.
type albumCreateRequest struct {
	Title         string   `json:"title"`
	DiscussionIDs []string `json:"discussion_ids"`
}

// albumAddMembersRequest is the body of POST /api/albums/{id}/discussions.
type albumAddMembersRequest struct {
	DiscussionIDs []string `json:"discussion_ids"`
}

// albumPublishRequest is the body of POST /api/albums/{id}/publish.
type albumPublishRequest struct {
	Mode          string          `json:"mode"`
	DiscussionIDs []string        `json:"discussion_ids"`
	Cover         DiscussionCover `json:"cover"`
}

// albumDetailResponse is the payload of GET /api/albums/{id}.
type albumDetailResponse struct {
	Album    *Album       `json:"album"`
	Episodes []Discussion `json:"episodes"`
}

// handleAlbumList serves GET /api/albums: the owner's albums, newest first.
func (s *Server) handleAlbumList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	albums, err := s.d.Discussions.ListAlbums(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range albums {
		s.refreshAlbumCoverURL(r.Context(), &albums[i])
	}
	writeJSON(w, albums)
}

// handleAlbumCreate serves POST /api/albums: a manual album grouping the given
// owned podcasts. An empty title defaults to the first member's title.
func (s *Server) handleAlbumCreate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req albumCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	cover := DiscussionCover{}
	members := make([]*Discussion, 0, len(req.DiscussionIDs))
	for _, id := range req.DiscussionIDs {
		d, err := s.d.Discussions.Get(r.Context(), user.ID, strings.TrimSpace(id))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.Error(w, "discussion not found: "+id, http.StatusNotFound)
			return
		}
		if d.AlbumID != "" {
			http.Error(w, errAlbumConflict.Error(), http.StatusBadRequest)
			return
		}
		members = append(members, d)
	}
	if title == "" && len(members) > 0 {
		title = strings.TrimSpace(members[0].Title)
		if title == "" {
			title = strings.TrimSpace(members[0].Topic)
		}
	}
	if title == "" {
		http.Error(w, "album title is required", http.StatusBadRequest)
		return
	}
	if len(members) > 0 {
		cover = members[0].Cover
	}
	album, err := s.d.Discussions.CreateAlbum(r.Context(), user.ID, title, albumKindManual, "", cover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, m := range members {
		pos := int64(0)
		if m.Script != nil {
			pos = albumPositionFor(m.Script.AudioBookChapterIndices)
		}
		if err := s.addToAlbumHTTP(w, r.Context(), user.ID, album.ID, m.ID, pos); err != nil {
			return
		}
	}
	album, err = s.d.Discussions.GetAlbum(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, album)
}

// addToAlbumHTTP adds one member and writes the HTTP error on failure,
// returning the error so callers can stop.
func (s *Server) addToAlbumHTTP(w http.ResponseWriter, ctx context.Context, owner, albumID, discussionID string, pos int64) error {
	err := s.d.Discussions.AddDiscussionToAlbum(ctx, owner, albumID, discussionID, pos)
	if errors.Is(err, errAlbumConflict) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}
	return nil
}

// handleAlbumGet serves GET /api/albums/{id}: the album plus its episodes in
// album order.
func (s *Server) handleAlbumGet(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	episodes, err := s.d.Discussions.AlbumEpisodes(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	timer := newStationTimer()
	s.prepareDiscussionListRows(r, episodes, timer)
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, albumDetailResponse{Album: album, Episodes: episodes})
}

// handleMarketAlbumGet serves GET /api/market/albums/{id}: the public album
// projection. Owners see the full album; everyone else sees only public
// ready/generating episodes.
func (s *Server) handleMarketAlbumGet(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	s.rememberCreatorProfile(r.Context(), user)
	album, err := s.d.Discussions.GetVisibleAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	episodes, err := s.d.Discussions.VisibleAlbumEpisodes(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !album.IsOwner && len(episodes) == 0 {
		http.NotFound(w, r)
		return
	}
	timer := newStationTimer()
	s.prepareMarketDiscussions(r, episodes, timer)
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, albumDetailResponse{Album: album, Episodes: episodes})
}

// handleAlbumAddMembers serves POST /api/albums/{id}/discussions: adds owned
// podcasts to an owned album. 400 when a podcast already belongs to another
// album.
func (s *Server) handleAlbumAddMembers(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req albumAddMembersRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if len(req.DiscussionIDs) == 0 {
		http.Error(w, "select at least one podcast", http.StatusBadRequest)
		return
	}
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	for _, id := range req.DiscussionIDs {
		d, err := s.d.Discussions.Get(r.Context(), user.ID, strings.TrimSpace(id))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if d == nil {
			http.Error(w, "discussion not found: "+id, http.StatusNotFound)
			return
		}
		pos := int64(0)
		if d.Script != nil {
			pos = albumPositionFor(d.Script.AudioBookChapterIndices)
		}
		if err := s.addToAlbumHTTP(w, r.Context(), user.ID, album.ID, d.ID, pos); err != nil {
			return
		}
	}
	album, err = s.d.Discussions.GetAlbum(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, album)
}

// handleAlbumUIActions serves GET /api/albums/{id}/ui-actions: the
// server-rendered album toolbar menu, mirroring the podcast toolbars so
// clients render whatever the server decides an album can do.
func (s *Server) handleAlbumUIActions(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	items := make([]discussionUIActionItem, 0, 4)
	if rootID, pending := s.albumPendingChapters(r.Context(), user.ID, album); pending > 0 && rootID != "" {
		items = append(items, actionItem("generate-more-chapters",
			phrase(lang, "Generate More Chapters", "继续生成章节", "繼續產生章節"), "",
			"text.badge.plus", "", true, "open-sheet", albumActionLink(album.ID, "sheet", "generate-chapters")))
	}
	items = append(items,
		actionItem("publish-album", phrase(lang, "Publish Album", "发布专辑", "發佈專輯"), "",
			"globe", "", album.EpisodeCount > 0, "open-sheet", albumActionLink(album.ID, "sheet", "publish")),
		actionItem("add-podcasts", phrase(lang, "Add Podcasts", "加入播客", "加入播客"), "",
			"plus.rectangle.on.folder", "", true, "open-sheet", albumActionLink(album.ID, "sheet", "add-podcasts")),
		actionItem("rename-album", phrase(lang, "Rename Album", "重命名专辑", "重新命名專輯"), "",
			"pencil", "", true, "open-sheet", albumActionLink(album.ID, "sheet", "rename")),
		actionItem("edit-cover", phrase(lang, "Edit Cover", "编辑封面", "編輯封面"), "",
			"photo.badge.plus", "", true, "open-sheet", albumActionLink(album.ID, "sheet", "cover")),
		actionItem("remove-album", phrase(lang, "Remove Album", "移除专辑", "移除專輯"), "",
			"rectangle.stack.badge.minus", "destructive", true, "request", albumActionLink(album.ID, "action", "remove")),
	)
	writeJSON(w, discussionUIActionsResponse{ID: "album-actions", Items: items})
}

// handleAlbumPublish serves POST /api/albums/{id}/publish: publishes all or a
// selected subset of owned album members. The album itself is visible in market
// whenever at least one member is public; private members stay hidden.
func (s *Server) handleAlbumPublish(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req albumPublishRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if !req.Cover.Valid() {
		http.Error(w, "cover is required to publish", http.StatusBadRequest)
		return
	}
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	episodes, err := s.d.Discussions.AlbumEpisodes(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(episodes) == 0 {
		http.Error(w, "album has no podcasts to publish", http.StatusBadRequest)
		return
	}

	selected, err := selectedAlbumEpisodes(req, episodes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.d.Discussions.SetAlbumCover(r.Context(), user.ID, album.ID, req.Cover); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, episode := range selected {
		cover := episode.Cover
		if !cover.Valid() {
			cover = req.Cover
		}
		if _, err := s.d.Discussions.SetVisibility(r.Context(), user.ID, episode.ID, DiscussionPublic, cover); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	album, err = s.d.Discussions.GetAlbum(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	episodes, err = s.d.Discussions.AlbumEpisodes(r.Context(), user.ID, album.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	timer := newStationTimer()
	s.prepareDiscussionListRows(r, episodes, timer)
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, albumDetailResponse{Album: album, Episodes: episodes})
}

func selectedAlbumEpisodes(req albumPublishRequest, episodes []Discussion) ([]Discussion, error) {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" || mode == "all" {
		return episodes, nil
	}
	if mode != "selected" {
		return nil, errors.New("invalid publish mode")
	}
	if len(req.DiscussionIDs) == 0 {
		return nil, errors.New("select at least one podcast")
	}
	byID := make(map[string]Discussion, len(episodes))
	for _, episode := range episodes {
		byID[episode.ID] = episode
	}
	out := make([]Discussion, 0, len(req.DiscussionIDs))
	seen := map[string]bool{}
	for _, raw := range req.DiscussionIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		episode, ok := byID[id]
		if !ok {
			return nil, errors.New("podcast is not in this album: " + id)
		}
		seen[id] = true
		out = append(out, episode)
	}
	if len(out) == 0 {
		return nil, errors.New("select at least one podcast")
	}
	return out, nil
}

// albumPendingChapters resolves the album's audiobook root (the auto album's
// recorded root, or the first audiobook member) and counts its still-pending
// chapters — gating the "Generate More Chapters" album action.
func (s *Server) albumPendingChapters(ctx context.Context, owner string, album *Album) (string, int) {
	rootID := strings.TrimSpace(album.RootDiscussionID)
	if rootID == "" {
		episodes, err := s.d.Discussions.AlbumEpisodes(ctx, owner, album.ID)
		if err != nil {
			return "", 0
		}
		for i := range episodes {
			if discussionIsAudioBook(&episodes[i]) {
				rootID = episodes[i].ID
				break
			}
		}
	}
	if rootID == "" {
		return "", 0
	}
	d, err := s.d.Discussions.Get(ctx, owner, rootID)
	if err != nil || d == nil {
		return "", 0
	}
	root, err := s.audioBookChapterRoot(ctx, owner, d)
	if err != nil || root == nil || !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) == 0 {
		return "", 0
	}
	states, err := s.audioBookChapterStates(ctx, owner, root, "")
	if err != nil {
		return "", 0
	}
	return root.ID, len(pendingChapterIndices(states))
}

// albumActionLink builds the deep-link the client validates and routes on:
// debatepod://album/{id}/{parts...}.
func albumActionLink(id string, parts ...string) string {
	escaped := make([]string, 0, len(parts)+1)
	escaped = append(escaped, url.PathEscape(id))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return "debatepod://album/" + strings.Join(escaped, "/")
}

// albumRenameRequest is the body of PATCH /api/albums/{id}.
type albumRenameRequest struct {
	Title string `json:"title"`
}

// handleAlbumRename serves PATCH /api/albums/{id}: renames an owned album.
func (s *Server) handleAlbumRename(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req albumRenameRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		http.Error(w, "album title is required", http.StatusBadRequest)
		return
	}
	album, err := s.d.Discussions.RenameAlbum(r.Context(), user.ID, r.PathValue("id"), req.Title)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, album)
}

// handleAlbumCoverSet serves PATCH /api/albums/{id}/cover: persists a cover
// (gradient, uploaded image, or a previously generated AI image) on an owned
// album, mirroring the discussion cover endpoint.
func (s *Server) handleAlbumCoverSet(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	var req marketCoverSetRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	album, err := s.d.Discussions.SetAlbumCover(r.Context(), user.ID, r.PathValue("id"), req.Cover)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	s.refreshAlbumCoverURL(r.Context(), album)
	writeJSON(w, album)
}

// handleAlbumCoverGenerate serves POST /api/albums/{id}/cover/generate:
// generates AI cover art for an owned album. Like the discussion counterpart
// it only returns the generated cover; the client persists it via
// PATCH /api/albums/{id}/cover.
func (s *Server) handleAlbumCoverGenerate(w http.ResponseWriter, r *http.Request) {
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		http.Error(w, "cover generation requires S3 storage", http.StatusServiceUnavailable)
		return
	}
	user := s.requestUser(r)
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	var req marketCoverGenerateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Square podcast album cover artwork for " + album.Title
	}

	reserved, reserveLedgerID, ok := s.reserveImageGeneration(w, r, user.ID, album.ID)
	if !ok {
		return
	}
	cover, err := s.generateStationCover(r.Context(), user.ID, album.ID, prompt)
	if err != nil {
		s.refundImageGeneration(r.Context(), user.ID, album.ID, reserved, reserveLedgerID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.settleImageGeneration(r.Context(), user.ID, album.ID, reserved, reserveLedgerID)
	writeJSON(w, marketCoverGenerateResponse{Cover: cover})
}

// handleAlbumDelete serves DELETE /api/albums/{id}: removes the album grouping
// and ungroups its members (podcasts are kept).
func (s *Server) handleAlbumDelete(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.d.Discussions.DisbandAlbum(r.Context(), user.ID, album.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlbumRemoveMember serves
// DELETE /api/albums/{id}/discussions/{discussionID}.
func (s *Server) handleAlbumRemoveMember(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	album, err := s.d.Discussions.GetAlbum(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if album == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.d.Discussions.RemoveDiscussionFromAlbum(r.Context(), user.ID, album.ID, r.PathValue("discussionID")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
