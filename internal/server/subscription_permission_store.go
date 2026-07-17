package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Permissions is the nested entitlement object attached to a subscription class.
// It decides which studios a user may create, which per-feature actions are
// available, and which models/voices they may pick. It is stored as a single
// JSON blob per class in subscription_permissions.permissions and returned
// verbatim to the iOS client by GET /api/entitlements.
type Permissions struct {
	Studios  PermissionStudios  `json:"studios"`
	Features PermissionFeatures `json:"features"`
	Models   PermissionRule     `json:"models"`
	Voices   PermissionRule     `json:"voices"`
	Limits   PermissionLimits   `json:"limits"`
}

// PermissionLimits holds per-tier numeric quotas.
type PermissionLimits struct {
	// MaxUploadAudioMB caps one uploaded podcast audio file in MiB for this
	// tier. 0 means "no tier-specific cap" — the env-wide
	// MAX_PODCAST_AUDIO_UPLOAD_MB ceiling still applies.
	MaxUploadAudioMB int64 `json:"maxUploadAudioMB"`
}

// PermissionStudios gates which content types a user may create.
type PermissionStudios struct {
	Discussion bool `json:"discussion"`
	AudioBook  bool `json:"audioBook"`
	Album      bool `json:"album"` // podcast / station album
}

// PermissionFeatures gates the per-discussion feature actions surfaced in the
// app's menus (see discussion_ui_actions.go for the action ids these map to).
type PermissionFeatures struct {
	CanUseChat               bool `json:"canUseChat"`
	CanPublishPodcast        bool `json:"canPublishPodcast"`
	CanSharePodcastPrivately bool `json:"canSharePodcastPrivately"`
	CanGenerateVideo         bool `json:"canGenerateVideo"`
	CanGenerateSummary       bool `json:"canGenerateSummary"`
	CanExportToNotion        bool `json:"canExportToNotion"`
	CanGeneratePPT           bool `json:"canGeneratePPT"`
	CanGenerateMindmap       bool `json:"canGenerateMindmap"`
	CanGenerateCoverWithAI   bool `json:"canGenerateCoverWithAI"`
	CanUploadOwnAudio        bool `json:"canUploadOwnAudio"`
	CanTranslatePodcast      bool `json:"canTranslatePodcast"`
}

// PermissionRuleMode values for PermissionRule.Mode.
const (
	PermissionModeAll  = "all"
	PermissionModeOnly = "only"
)

// PermissionRule constrains a pickable catalog (models or voices). Mode "all"
// allows every catalog entry; mode "only" restricts to the Allow whitelist
// (model ids or Azure voice ShortNames).
type PermissionRule struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow"`
}

// Allows reports whether id is permitted by the rule. An "all" rule (or any
// unrecognised mode) allows everything; an "only" rule allows just the
// whitelisted ids.
func (r PermissionRule) Allows(id string) bool {
	if r.Mode != PermissionModeOnly {
		return true
	}
	for _, a := range r.Allow {
		if a == id {
			return true
		}
	}
	return false
}

// DefaultPermissions is the hard fallback used when no subscription class row is
// configured at all (not even a free class): nothing is allowed. Admins are
// expected to author a "free" class to grant baseline access.
func DefaultPermissions() Permissions {
	return Permissions{
		Models: PermissionRule{Mode: PermissionModeOnly, Allow: []string{}},
		Voices: PermissionRule{Mode: PermissionModeOnly, Allow: []string{}},
	}
}

func (p *Permissions) normalize() {
	if p.Models.Mode != PermissionModeAll {
		p.Models.Mode = PermissionModeOnly
	}
	if p.Voices.Mode != PermissionModeAll {
		p.Voices.Mode = PermissionModeOnly
	}
	if p.Models.Allow == nil {
		p.Models.Allow = []string{}
	}
	if p.Voices.Allow == nil {
		p.Voices.Allow = []string{}
	}
}

// SubscriptionPermission is a subscription-class → permissions mapping row. The
// class is identified by (product_id, store_environment); the free / no-
// subscription class uses the empty sentinel for both.
type SubscriptionPermission struct {
	ID               int64       `json:"id" jsonschema:"title=ID" table:"order=0;pinned=true"`
	ProductID        string      `json:"product_id" jsonschema:"title=Product ID" table:"order=1;pinned=true"`
	StoreEnvironment string      `json:"store_environment" jsonschema:"title=Store" table:"order=2;format=chip"`
	Permissions      Permissions `json:"permissions" table:"omit=true"`
	CreatedAt        time.Time   `json:"created_at" table:"order=3;format=date-time"`
	UpdatedAt        time.Time   `json:"updated_at" table:"omit=true"`
}

// SubscriptionPermissionStore persists the subscription-class → permissions map
// in the discussion DB, alongside the IAP catalog.
type SubscriptionPermissionStore struct {
	db *sqlDB
}

// NewSubscriptionPermissionStore builds the store on the DiscussionStore's
// shared handle and ensures its schema exists.
func NewSubscriptionPermissionStore(ds *DiscussionStore) (*SubscriptionPermissionStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("subscription permission store requires a discussion store")
	}
	s := &SubscriptionPermissionStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SubscriptionPermissionStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS subscription_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			product_id TEXT NOT NULL DEFAULT '',
			store_environment TEXT NOT NULL DEFAULT '',
			permissions TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(store_environment, product_id)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

const subscriptionPermissionColumns = `id, product_id, store_environment, permissions, created_at, updated_at`

type subscriptionPermissionScanner interface {
	Scan(dest ...any) error
}

func scanSubscriptionPermission(row subscriptionPermissionScanner) (SubscriptionPermission, error) {
	var p SubscriptionPermission
	var permsJSON string
	var created, updated int64
	if err := row.Scan(&p.ID, &p.ProductID, &p.StoreEnvironment, &permsJSON, &created, &updated); err != nil {
		return p, err
	}
	if strings.TrimSpace(permsJSON) != "" {
		if err := json.Unmarshal([]byte(permsJSON), &p.Permissions); err != nil {
			return p, err
		}
	}
	p.Permissions.normalize()
	p.CreatedAt = time.UnixMilli(created)
	p.UpdatedAt = time.UnixMilli(updated)
	return p, nil
}

// List returns a cursor-paginated page of class rows ordered by id.
func (s *SubscriptionPermissionStore) List(ctx context.Context, after int64, limit int) (rows []SubscriptionPermission, nextCursor int64, err error) {
	if s == nil {
		return nil, 0, errors.New("subscription permission store is not configured")
	}
	if limit <= 0 {
		limit = 20
	}
	result, err := s.db.QueryContext(ctx, `SELECT `+subscriptionPermissionColumns+`
		FROM subscription_permissions
		WHERE (? = 0 OR id > ?)
		ORDER BY id ASC
		LIMIT ?`, after, after, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer result.Close()
	for result.Next() {
		p, err := scanSubscriptionPermission(result)
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, p)
	}
	if err := result.Err(); err != nil {
		return nil, 0, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		nextCursor = rows[len(rows)-1].ID
	}
	return rows, nextCursor, nil
}

// Get returns the row by id, or nil when it does not exist.
func (s *SubscriptionPermissionStore) Get(ctx context.Context, id int64) (*SubscriptionPermission, error) {
	if s == nil {
		return nil, errors.New("subscription permission store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+subscriptionPermissionColumns+` FROM subscription_permissions WHERE id = ?`, id)
	p, err := scanSubscriptionPermission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetForClass returns the permissions for a subscription class identified by
// (productID, storeEnvironment), or nil when no row is configured for it. Pass
// empty strings to fetch the free / no-subscription class.
func (s *SubscriptionPermissionStore) GetForClass(ctx context.Context, productID, storeEnvironment string) (*SubscriptionPermission, error) {
	if s == nil {
		return nil, errors.New("subscription permission store is not configured")
	}
	productID = strings.TrimSpace(productID)
	storeEnvironment = normalizeIAPStoreEnvironment(storeEnvironment)
	row := s.db.QueryRowContext(ctx, `SELECT `+subscriptionPermissionColumns+`
		FROM subscription_permissions WHERE product_id = ? AND store_environment = ?`, productID, storeEnvironment)
	p, err := scanSubscriptionPermission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetFree returns the free / no-subscription class (empty sentinel), or nil when
// unconfigured.
func (s *SubscriptionPermissionStore) GetFree(ctx context.Context) (*SubscriptionPermission, error) {
	return s.GetForClass(ctx, "", "")
}

// Create inserts a new class row. The free class uses empty product_id and
// store_environment.
func (s *SubscriptionPermissionStore) Create(ctx context.Context, p *SubscriptionPermission) error {
	if s == nil {
		return errors.New("subscription permission store is not configured")
	}
	s.normalize(p)
	permsJSON, err := json.Marshal(p.Permissions)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `INSERT INTO subscription_permissions
		(product_id, store_environment, permissions, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		p.ProductID, p.StoreEnvironment, string(permsJSON), now, now)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = time.UnixMilli(now)
	p.UpdatedAt = p.CreatedAt
	return nil
}

// Update rewrites a class row by id.
func (s *SubscriptionPermissionStore) Update(ctx context.Context, id int64, p *SubscriptionPermission) error {
	if s == nil {
		return errors.New("subscription permission store is not configured")
	}
	s.normalize(p)
	permsJSON, err := json.Marshal(p.Permissions)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE subscription_permissions SET
		product_id = ?, store_environment = ?, permissions = ?, updated_at = ?
		WHERE id = ?`,
		p.ProductID, p.StoreEnvironment, string(permsJSON), now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	p.ID = id
	p.UpdatedAt = time.UnixMilli(now)
	return nil
}

// Delete removes a class row by id.
func (s *SubscriptionPermissionStore) Delete(ctx context.Context, id int64) error {
	if s == nil {
		return errors.New("subscription permission store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM subscription_permissions WHERE id = ?`, id)
	return err
}

// normalize trims/normalizes the class key. The free class keeps empty
// product_id + store_environment; any other row must carry a product id and a
// normalized store environment.
func (s *SubscriptionPermissionStore) normalize(p *SubscriptionPermission) {
	p.ProductID = strings.TrimSpace(p.ProductID)
	if p.ProductID == "" {
		// Free / no-subscription class: force the empty sentinel on both key
		// columns so it round-trips through GetFree.
		p.StoreEnvironment = ""
	} else {
		p.StoreEnvironment = normalizeIAPStoreEnvironment(p.StoreEnvironment)
	}
	p.Permissions.normalize()
}
