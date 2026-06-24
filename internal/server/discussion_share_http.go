package server

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// maxShareTTL bounds how long a share link may stay valid. The client offers
// presets up to this ceiling (1h … 72h); anything larger is clamped.
const maxShareTTL = 72 * time.Hour

type shareCreateRequest struct {
	TTLSeconds int64 `json:"ttl_seconds"`
}

type shareResponse struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type joinRequest struct {
	Token string `json:"token"`
}

// shareResolveResponse is the trimmed metadata the website renders OG tags and
// an "open in app" card from. It deliberately omits the transcript and any
// owner-only fields.
type shareResolveResponse struct {
	ID         string               `json:"id"`
	Title      string               `json:"title"`
	Topic      string               `json:"topic"`
	Visibility DiscussionVisibility `json:"visibility"`
	Cover      DiscussionCover      `json:"cover"`
	Creator    *CreatorProfile      `json:"creator,omitempty"`
	ExpiresAt  *time.Time           `json:"expires_at,omitempty"`
}

func (s *Server) shareURL(token string) string {
	base := strings.TrimRight(strings.TrimSpace(s.d.WebsiteBaseURL), "/")
	if base == "" {
		base = "https://podcast.rxlab.app"
	}
	return base + "/s/" + token
}

// handleDiscussionShareCreate mints an expiring share link for an owned PRIVATE
// discussion. Public discussions don't need a tracked link (the client shares
// the plain /d/{id} URL), so this returns 409 for them.
func (s *Server) handleDiscussionShareCreate(w http.ResponseWriter, r *http.Request) {
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
	if d.Visibility == DiscussionPublic {
		http.Error(w, "public discussions are shared via their public link", http.StatusConflict)
		return
	}
	var req shareCreateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		http.Error(w, "ttl_seconds must be positive", http.StatusBadRequest)
		return
	}
	if ttl > maxShareTTL {
		ttl = maxShareTTL
	}
	share, err := s.d.Discussions.CreateShare(r.Context(), user.ID, id, ttl)
	if err != nil {
		writeDiscussionAccessError(w, err)
		return
	}
	writeJSON(w, shareResponse{
		Token:     share.Token,
		URL:       s.shareURL(share.Token),
		CreatedAt: share.CreatedAt,
		ExpiresAt: share.ExpiresAt,
	})
}

// handleDiscussionShareList returns the active share links for an owned
// discussion so the share sheet can list and revoke them.
func (s *Server) handleDiscussionShareList(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	if ok, err := s.d.Discussions.owns(r.Context(), user.ID, id); err != nil || !ok {
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
		return
	}
	shares, err := s.d.Discussions.ListSharesForDiscussion(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]shareResponse, 0, len(shares))
	for _, sh := range shares {
		out = append(out, shareResponse{
			Token:     sh.Token,
			URL:       s.shareURL(sh.Token),
			CreatedAt: sh.CreatedAt,
			ExpiresAt: sh.ExpiresAt,
		})
	}
	writeJSON(w, out)
}

// handleDiscussionShareRevoke revokes an owner's share token.
func (s *Server) handleDiscussionShareRevoke(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	token := r.PathValue("token")
	ok, err := s.d.Discussions.RevokeShare(r.Context(), user.ID, id, token)
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

// handleDiscussionJoin records the caller as a participant, enforcing the
// per-discussion cap. A share token (body) lets a non-owner join a private
// discussion; without one, only the owner or a participant of a public
// discussion may join. Returns 409 when the discussion is full.
func (s *Server) handleDiscussionJoin(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	var req joinRequest
	if r.ContentLength != 0 {
		if !decodeJSONBody(w, r, &req) {
			return
		}
	}
	owner, visibility, err := s.d.Discussions.ownerAndVisibility(r.Context(), id)
	if err != nil {
		writeDiscussionAccessError(w, err)
		return
	}
	// Authorize: owner always; valid token for this discussion; or public.
	authorized := user.ID == owner || visibility == DiscussionPublic
	if !authorized && strings.TrimSpace(req.Token) != "" {
		if resolvedID, rerr := s.d.Discussions.ResolveShare(r.Context(), req.Token); rerr == nil && resolvedID == id {
			authorized = true
		}
	}
	if !authorized {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.d.Discussions.JoinDiscussion(r.Context(), id, owner, user.ID); err != nil {
		if errors.Is(err, errParticipantCapReached) {
			http.Error(w, "discussion is full", http.StatusConflict)
			return
		}
		writeDiscussionAccessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleShareJoin is the iOS / App Clip entry for a private share link: a
// signed-in user POSTs the token, which is resolved, the cap is enforced, the
// caller is recorded as a participant, and the full discussion (with transcript)
// is returned so the client can open the player. Returns 410 for an
// expired/revoked/unknown link and 409 when the discussion is full.
func (s *Server) handleShareJoin(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	token := r.PathValue("token")
	id, err := s.d.Discussions.ResolveShare(r.Context(), token)
	if err != nil {
		if errors.Is(err, errDiscussionNotVisible) {
			http.Error(w, "share link expired", http.StatusGone)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner, _, err := s.d.Discussions.ownerAndVisibility(r.Context(), id)
	if err != nil {
		writeDiscussionAccessError(w, err)
		return
	}
	if err := s.d.Discussions.JoinDiscussion(r.Context(), id, owner, user.ID); err != nil {
		if errors.Is(err, errParticipantCapReached) {
			http.Error(w, "discussion is full", http.StatusConflict)
			return
		}
		writeDiscussionAccessError(w, err)
		return
	}
	d, err := s.d.Discussions.GetForShareWithLines(r.Context(), id, user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.Error(w, "share link expired", http.StatusGone)
		return
	}
	s.applyDiscussionJobStatus(r, d, true)
	s.refreshDiscussionCoverURL(r.Context(), d)
	s.refreshDiscussionLineAudioURLs(r.Context(), d)
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

// handleShareResolve returns the trimmed metadata used by the website and App
// Clip to render a share-link entry surface. The unguessable share token is the
// capability here; full transcript access still requires a signed-in POST to
// /api/share/{token}/join. Returns 410 when the link is expired/revoked/unknown.
func (s *Server) handleShareResolve(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	id, err := s.d.Discussions.ResolveShare(r.Context(), token)
	if err != nil {
		if errors.Is(err, errDiscussionNotVisible) {
			http.Error(w, "share link expired", http.StatusGone)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d, err := s.d.Discussions.GetForShare(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.Error(w, "share link expired", http.StatusGone)
		return
	}
	s.refreshDiscussionCoverURL(r.Context(), d)
	writeJSON(w, shareResolveResponse{
		ID:         d.ID,
		Title:      d.DisplayTitle(),
		Topic:      d.Topic,
		Visibility: d.Visibility,
		Cover:      d.Cover,
		Creator:    d.Creator,
	})
}
