package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Album kinds. Auto albums are created implicitly around a root discussion
// when its first follow-up (chapter batch or follow-up podcast) appears;
// manual albums are user-created groupings.
const (
	albumKindAuto   = "auto"
	albumKindManual = "manual"
)

// Album groups linked podcasts into one home-list entry with a shared title
// and cover. Episodes are native_discussions rows carrying this album's id.
type Album struct {
	ID               string          `json:"id"`
	OwnerUserID      string          `json:"-"`
	Title            string          `json:"title"`
	Kind             string          `json:"kind"`
	RootDiscussionID string          `json:"root_discussion_id,omitempty"`
	Cover            DiscussionCover `json:"cover,omitempty"`
	EpisodeCount     int64           `json:"episode_count"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// AlbumSummary is the compact album descriptor attached to discussion list
// rows so the home screen can group them without extra requests.
type AlbumSummary struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Kind         string          `json:"kind"`
	Cover        DiscussionCover `json:"cover,omitempty"`
	EpisodeCount int64           `json:"episode_count"`
}

var errAlbumConflict = errors.New("podcast already belongs to another album")

const albumSelectColumns = `a.id, a.owner_user_id, a.title, a.kind, a.root_discussion_id,
	a.cover_type, a.cover_image_url, a.cover_image_key, a.cover_gradient_start, a.cover_gradient_end,
	a.created_at, a.updated_at,
	(SELECT COUNT(*) FROM native_discussions d WHERE d.album_id = a.id) AS episode_count`

func scanAlbum(row discussionScanner) (Album, error) {
	var a Album
	var created, updated int64
	err := row.Scan(&a.ID, &a.OwnerUserID, &a.Title, &a.Kind, &a.RootDiscussionID,
		&a.Cover.Type, &a.Cover.ImageURL, &a.Cover.ImageKey, &a.Cover.GradientStart, &a.Cover.GradientEnd,
		&created, &updated, &a.EpisodeCount)
	if err != nil {
		return a, err
	}
	a.CreatedAt = time.UnixMilli(created)
	a.UpdatedAt = time.UnixMilli(updated)
	return a, nil
}

// CreateAlbum inserts a new album owned by owner and returns it.
func (s *DiscussionStore) CreateAlbum(ctx context.Context, owner, title, kind, rootDiscussionID string, cover DiscussionCover) (*Album, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	if kind != albumKindAuto {
		kind = albumKindManual
	}
	id := newJobID()
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO native_albums
		(id, owner_user_id, title, kind, root_discussion_id, cover_type, cover_image_url, cover_image_key,
		 cover_gradient_start, cover_gradient_end, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, owner, strings.TrimSpace(title), kind, strings.TrimSpace(rootDiscussionID),
		cover.Type, cover.ImageURL, cover.ImageKey, cover.GradientStart, cover.GradientEnd, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetAlbum(ctx, owner, id)
}

// GetAlbum returns an owned album with its episode count, or nil when absent.
func (s *DiscussionStore) GetAlbum(ctx context.Context, owner, id string) (*Album, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+albumSelectColumns+`
		FROM native_albums a WHERE a.owner_user_id = ? AND a.id = ?`, owner, strings.TrimSpace(id))
	a, err := scanAlbum(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// ListAlbums returns the owner's albums, most recently updated first.
func (s *DiscussionStore) ListAlbums(ctx context.Context, owner string) ([]Album, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+albumSelectColumns+`
		FROM native_albums a WHERE a.owner_user_id = ? ORDER BY a.updated_at DESC, a.id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Album, 0)
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlbumEpisodes returns the discussions in an owned album ordered by album
// position (chapter order for audiobook batches), then creation time. The full
// column set is selected so episode rows carry their scripts (chapter ranges).
func (s *DiscussionStore) AlbumEpisodes(ctx context.Context, owner, albumID string) ([]Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions
		WHERE owner_user_id = ? AND album_id = ?
		ORDER BY album_position ASC, created_at ASC, id ASC`, owner, strings.TrimSpace(albumID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussion(rows)
		if err != nil {
			return nil, err
		}
		markDiscussionViewer(&d, owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddDiscussionToAlbum places an owned discussion into an owned album at the
// given position. Returns errAlbumConflict when the discussion already belongs
// to a different album (move requires removing it first).
func (s *DiscussionStore) AddDiscussionToAlbum(ctx context.Context, owner, albumID, discussionID string, position int64) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	albumID = strings.TrimSpace(albumID)
	discussionID = strings.TrimSpace(discussionID)
	var current string
	err := s.db.QueryRowContext(ctx, `SELECT album_id FROM native_discussions
		WHERE owner_user_id = ? AND id = ?`, owner, discussionID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("discussion not found: %s", discussionID)
	}
	if err != nil {
		return err
	}
	if current != "" && current != albumID {
		return errAlbumConflict
	}
	now := time.Now().UnixMilli()
	if _, err := s.exec(ctx, `UPDATE native_discussions SET album_id = ?, album_position = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`, albumID, position, now, owner, discussionID); err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE native_albums SET updated_at = ? WHERE owner_user_id = ? AND id = ?`,
		now, owner, albumID)
	return err
}

// RemoveDiscussionFromAlbum detaches a discussion from an owned album. The
// album row is kept even when it empties (the owner may re-add members).
func (s *DiscussionStore) RemoveDiscussionFromAlbum(ctx context.Context, owner, albumID, discussionID string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	now := time.Now().UnixMilli()
	if _, err := s.exec(ctx, `UPDATE native_discussions SET album_id = '', album_position = 0, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND album_id = ?`,
		now, owner, strings.TrimSpace(discussionID), strings.TrimSpace(albumID)); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE native_albums SET updated_at = ? WHERE owner_user_id = ? AND id = ?`,
		now, owner, strings.TrimSpace(albumID))
	return err
}

// RenameAlbum updates an owned album's title.
func (s *DiscussionStore) RenameAlbum(ctx context.Context, owner, id, title string) (*Album, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("album title is required")
	}
	res, err := s.exec(ctx, `UPDATE native_albums SET title = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		title, time.Now().UnixMilli(), owner, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.GetAlbum(ctx, owner, id)
}

// SetAlbumCover updates an owned album's cover columns, leaving the rest of
// the album untouched.
func (s *DiscussionStore) SetAlbumCover(ctx context.Context, owner, id string, cover DiscussionCover) (*Album, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	res, err := s.exec(ctx, `UPDATE native_albums SET cover_type = ?, cover_image_url = ?, cover_image_key = ?,
		cover_gradient_start = ?, cover_gradient_end = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		cover.Type, cover.ImageURL, cover.ImageKey, cover.GradientStart, cover.GradientEnd,
		time.Now().UnixMilli(), owner, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.GetAlbum(ctx, owner, id)
}

// DisbandAlbum deletes an owned album and ungroups its members — the podcasts
// themselves are kept.
func (s *DiscussionStore) DisbandAlbum(ctx context.Context, owner, id string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	id = strings.TrimSpace(id)
	now := time.Now().UnixMilli()
	if _, err := s.exec(ctx, `UPDATE native_discussions SET album_id = '', album_position = 0, updated_at = ?
		WHERE owner_user_id = ? AND album_id = ?`, now, owner, id); err != nil {
		return err
	}
	_, err := s.exec(ctx, `DELETE FROM native_albums WHERE owner_user_id = ? AND id = ?`, owner, id)
	return err
}

// AlbumSummariesFor returns compact summaries for the given album ids owned by
// owner, in one grouped query — used to annotate discussion list pages.
func (s *DiscussionStore) AlbumSummariesFor(ctx context.Context, owner string, ids []string) (map[string]*AlbumSummary, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	distinct := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		distinct = append(distinct, id)
	}
	if len(distinct) == 0 {
		return map[string]*AlbumSummary{}, nil
	}
	placeholders := strings.Repeat("?,", len(distinct))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(distinct)+1)
	args = append(args, owner)
	for _, id := range distinct {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+albumSelectColumns+`
		FROM native_albums a WHERE a.owner_user_id = ? AND a.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*AlbumSummary, len(distinct))
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out[a.ID] = &AlbumSummary{ID: a.ID, Title: a.Title, Kind: a.Kind, Cover: a.Cover, EpisodeCount: a.EpisodeCount}
	}
	return out, rows.Err()
}
