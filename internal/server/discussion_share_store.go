package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// errParticipantCapReached is returned by JoinDiscussion when admitting the
// caller would exceed config.MaxParticipantsPerDiscussion distinct non-owner
// participants.
var errParticipantCapReached = errors.New("participant cap reached")

// DiscussionShare is an expiring, revocable link granting non-owners
// participation rights on a private discussion. URL is filled by the HTTP layer
// from the configured website base; it is not stored.
type DiscussionShare struct {
	Token        string    `json:"token"`
	DiscussionID string    `json:"discussion_id"`
	URL          string    `json:"url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// newShareToken returns a URL-safe, unguessable share token.
func newShareToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreateShare mints a new share token for an owned discussion. ttl is clamped by
// the caller; here it is trusted. Ownership is verified so a non-owner can never
// create a link for a discussion they don't control.
func (s *DiscussionStore) CreateShare(ctx context.Context, ownerUserID, discussionID string, ttl time.Duration) (DiscussionShare, error) {
	if s == nil {
		return DiscussionShare{}, errors.New("discussion store is not configured")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	discussionID = strings.TrimSpace(discussionID)
	if ok, err := s.owns(ctx, ownerUserID, discussionID); err != nil || !ok {
		if err != nil {
			return DiscussionShare{}, err
		}
		return DiscussionShare{}, errDiscussionForbidden
	}
	token, err := newShareToken()
	if err != nil {
		return DiscussionShare{}, err
	}
	now := time.Now()
	expires := now.Add(ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO native_discussion_shares
		(token, discussion_id, owner_user_id, created_at, expires_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, 0)`,
		token, discussionID, ownerUserID, now.UnixMilli(), expires.UnixMilli())
	if err != nil {
		return DiscussionShare{}, err
	}
	return DiscussionShare{
		Token:        token,
		DiscussionID: discussionID,
		CreatedAt:    now,
		ExpiresAt:    expires,
	}, nil
}

// ListSharesForDiscussion returns the still-active (not revoked, not expired)
// shares an owner created for a discussion, newest first.
func (s *DiscussionStore) ListSharesForDiscussion(ctx context.Context, ownerUserID, discussionID string) ([]DiscussionShare, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token, discussion_id, created_at, expires_at
		FROM native_discussion_shares
		WHERE owner_user_id = ? AND discussion_id = ? AND revoked_at = 0 AND expires_at > ?
		ORDER BY created_at DESC`,
		strings.TrimSpace(ownerUserID), strings.TrimSpace(discussionID), time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DiscussionShare, 0)
	for rows.Next() {
		var sh DiscussionShare
		var created, expires int64
		if err := rows.Scan(&sh.Token, &sh.DiscussionID, &created, &expires); err != nil {
			return nil, err
		}
		sh.CreatedAt = time.UnixMilli(created)
		sh.ExpiresAt = time.UnixMilli(expires)
		out = append(out, sh)
	}
	return out, rows.Err()
}

// RevokeShare marks an owner's share token revoked. Returns false when no
// matching active share exists for that owner+discussion.
func (s *DiscussionStore) RevokeShare(ctx context.Context, ownerUserID, discussionID, token string) (bool, error) {
	if s == nil {
		return false, errors.New("discussion store is not configured")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussion_shares SET revoked_at = ?
		WHERE token = ? AND owner_user_id = ? AND discussion_id = ? AND revoked_at = 0`,
		time.Now().UnixMilli(), strings.TrimSpace(token), strings.TrimSpace(ownerUserID), strings.TrimSpace(discussionID))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResolveShare returns the discussion id a valid, unexpired, unrevoked token
// points to. It returns errDiscussionNotVisible when the token is unknown,
// expired, or revoked so callers can map it to a 404/410 without leaking which.
func (s *DiscussionStore) ResolveShare(ctx context.Context, token string) (string, error) {
	if s == nil {
		return "", errors.New("discussion store is not configured")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errDiscussionNotVisible
	}
	var discussionID string
	var expires, revoked int64
	err := s.db.QueryRowContext(ctx, `SELECT discussion_id, expires_at, revoked_at
		FROM native_discussion_shares WHERE token = ?`, token).Scan(&discussionID, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errDiscussionNotVisible
	}
	if err != nil {
		return "", err
	}
	if revoked != 0 || expires <= time.Now().UnixMilli() {
		return "", errDiscussionNotVisible
	}
	return discussionID, nil
}

// JoinDiscussion records userID as a participant of discussionID, enforcing the
// distinct-participant cap. The owner is admitted without consuming a slot and
// without being recorded. An already-recorded participant is always admitted
// (idempotent). The cap is checked atomically so concurrent joiners can't
// overshoot it.
func (s *DiscussionStore) JoinDiscussion(ctx context.Context, discussionID, ownerUserID, userID string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	discussionID = strings.TrimSpace(discussionID)
	userID = strings.TrimSpace(userID)
	if discussionID == "" || userID == "" {
		return errDiscussionNotVisible
	}
	if userID == strings.TrimSpace(ownerUserID) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO native_discussion_participants
		(discussion_id, user_id, joined_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		discussionID, userID, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already a participant — admit without re-checking the cap.
		return tx.Commit()
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM native_discussion_participants
		WHERE discussion_id = ?`, discussionID).Scan(&count); err != nil {
		return err
	}
	if count > config.MaxParticipantsPerDiscussion {
		return errParticipantCapReached
	}
	return tx.Commit()
}

// ownerAndVisibility returns the owner id and visibility of a discussion, or
// errDiscussionNotVisible when it doesn't exist.
func (s *DiscussionStore) ownerAndVisibility(ctx context.Context, id string) (string, DiscussionVisibility, error) {
	var owner, visibility string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id, visibility FROM native_discussions WHERE id = ?`,
		strings.TrimSpace(id)).Scan(&owner, &visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errDiscussionNotVisible
	}
	if err != nil {
		return "", "", err
	}
	if visibility == "" {
		visibility = string(DiscussionPrivate)
	}
	return owner, DiscussionVisibility(visibility), nil
}

// CountParticipants returns the number of distinct recorded (non-owner)
// participants for a discussion.
func (s *DiscussionStore) CountParticipants(ctx context.Context, discussionID string) (int, error) {
	if s == nil {
		return 0, errors.New("discussion store is not configured")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM native_discussion_participants
		WHERE discussion_id = ?`, strings.TrimSpace(discussionID)).Scan(&count)
	return count, err
}

// isParticipant reports whether userID has a participant record for the
// discussion (or is its owner).
func (s *DiscussionStore) isParticipant(ctx context.Context, discussionID, userID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM native_discussion_participants
		WHERE discussion_id = ? AND user_id = ?`,
		strings.TrimSpace(discussionID), strings.TrimSpace(userID)).Scan(&n)
	return n > 0, err
}

// AuthorizeShareParticipation authorizes a viewer to participate (comment) on a
// discussion. The owner is always allowed. Otherwise, if a valid share token
// resolves to this discussion and the viewer has joined it, they are allowed.
// Falls back to the visibility-based rule (public + generating) for non-share
// callers.
func (s *DiscussionStore) AuthorizeShareParticipation(ctx context.Context, viewer, id, token string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errDiscussionNotVisible
	}
	if strings.TrimSpace(token) != "" {
		resolvedID, err := s.ResolveShare(ctx, token)
		if err == nil && resolvedID == id {
			ok, err := s.isParticipant(ctx, id, viewer)
			if err != nil {
				return err
			}
			if ok {
				return s.ensureDiscussionAllowsSendingMessage(ctx, id)
			}
		}
	}
	return s.authorizeParticipation(ctx, viewer, id, "")
}

func (s *DiscussionStore) ensureDiscussionAllowsSendingMessage(ctx context.Context, id string) error {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM native_discussions WHERE id = ?`, strings.TrimSpace(id)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return errDiscussionNotVisible
	}
	if err != nil {
		return err
	}
	if !discussionAllowsSendingMessage(DiscussionStatus(status)) {
		return errDiscussionForbidden
	}
	return nil
}

// GetForShare loads a discussion by id with NO visibility filter. It is only
// reachable behind a resolved, valid share token (service-token callers) so it
// can surface a private discussion's metadata for OG rendering. Lines and edit
// turns are not loaded.
func (s *DiscussionStore) GetForShare(ctx context.Context, id string) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE id = ?`, strings.TrimSpace(id))
	d, err := scanDiscussion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if profile, err := s.CreatorProfile(ctx, d.OwnerUserID, d.OwnerUserID); err == nil {
		d.Creator = profile
	}
	return &d, nil
}

// GetForShareWithLines loads a discussion (no visibility filter) plus its
// transcript lines for a viewer who reached it via a valid share token. The
// viewer is marked as a non-owner participant; edit turns (owner-only) are not
// loaded.
func (s *DiscussionStore) GetForShareWithLines(ctx context.Context, id, viewer string) (*Discussion, error) {
	d, err := s.GetForShare(ctx, id)
	if err != nil || d == nil {
		return d, err
	}
	lines, err := s.lines(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	markDiscussionViewer(d, viewer)
	return d, nil
}
