package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	IAPStoreEnvironmentTest     = "test_store"
	IAPStoreEnvironmentAppStore = "app_store"

	IAPProductTypeConsumable    = "consumable"
	IAPProductTypeSubscription  = "subscription"
	IAPProductTypeNonConsumable = "non_consumable"
)

// IAPProduct is the admin-owned catalog row used to validate purchase webhooks
// and decide how many points a store product grants. App Store Connect remains
// the source of truth for the purchase UI price; this table is the backend
// source of truth for product ids and points grants.
type IAPProduct struct {
	ID                 int64      `json:"id" jsonschema:"title=ID" table:"order=0;pinned=true"`
	ProductID          string     `json:"product_id" jsonschema:"title=Product ID" table:"order=1;pinned=true"`
	StoreEnvironment   string     `json:"store_environment" jsonschema:"title=Store,enum=test_store,enum=app_store" table:"order=2;format=chip"`
	ProductType        string     `json:"product_type" jsonschema:"title=Type,enum=consumable,enum=subscription,enum=non_consumable" table:"order=3"`
	DisplayName        string     `json:"display_name" jsonschema:"title=Display name" table:"order=4"`
	PointsGrant        int64      `json:"points_grant" jsonschema:"title=Points granted" table:"order=5"`
	PriceCurrency      string     `json:"price_currency" jsonschema:"title=Currency" table:"order=6"`
	PriceMinorUnits    int64      `json:"price_minor_units" jsonschema:"title=Price (minor units)" table:"order=7"`
	SubscriptionPeriod string     `json:"subscription_period" jsonschema:"title=Subscription period" table:"omit=true"`
	LastSyncError      string     `json:"last_sync_error" jsonschema:"title=Last sync error" table:"omit=true"`
	SyncedAt           *time.Time `json:"synced_at,omitempty" jsonschema:"title=Synced at,format=date-time" table:"order=9;format=date-time"`
	Enabled            bool       `json:"enabled" jsonschema:"title=Enabled" table:"order=8;format=boolean"`
	CreatedAt          time.Time  `json:"created_at" jsonschema:"title=Created" table:"order=10;format=date-time"`
	UpdatedAt          time.Time  `json:"updated_at" table:"omit=true"`
}

type iapProductForm struct {
	ProductID          string `json:"product_id" jsonschema:"title=Product ID" validate:"required"`
	StoreEnvironment   string `json:"store_environment" jsonschema:"title=Store,enum=test_store,enum=app_store,default=test_store" validate:"required,oneof=test_store app_store"`
	ProductType        string `json:"product_type" jsonschema:"title=Type,enum=consumable,enum=subscription,enum=non_consumable,default=consumable" validate:"required,oneof=consumable subscription non_consumable"`
	DisplayName        string `json:"display_name,omitempty" jsonschema:"title=Display name"`
	PointsGrant        int64  `json:"points_grant" jsonschema:"title=Points granted" validate:"gte=0"`
	PriceCurrency      string `json:"price_currency,omitempty" jsonschema:"title=Currency"`
	PriceMinorUnits    int64  `json:"price_minor_units,omitempty" jsonschema:"title=Price (minor units)" validate:"gte=0"`
	SubscriptionPeriod string `json:"subscription_period,omitempty" jsonschema:"title=Subscription period,enum=ONE_WEEK,enum=ONE_MONTH,enum=TWO_MONTHS,enum=THREE_MONTHS,enum=SIX_MONTHS,enum=ONE_YEAR,default=ONE_MONTH"`
	Enabled            bool   `json:"enabled,omitempty" jsonschema:"title=Enabled,default=true"`
}

// IAPProductStore persists the store product catalog in the discussion DB.
type IAPProductStore struct {
	db *sqlDB
}

type iapProductExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func NewIAPProductStore(ds *DiscussionStore) (*IAPProductStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("iap product store requires a discussion store")
	}
	s := &IAPProductStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *IAPProductStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS iap_products (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			product_id TEXT NOT NULL,
			store_environment TEXT NOT NULL,
			product_type TEXT NOT NULL DEFAULT 'consumable',
			display_name TEXT NOT NULL DEFAULT '',
			points_grant INTEGER NOT NULL DEFAULT 0,
			price_currency TEXT NOT NULL DEFAULT '',
			price_minor_units INTEGER NOT NULL DEFAULT 0,
			subscription_period TEXT NOT NULL DEFAULT '',
			last_sync_error TEXT NOT NULL DEFAULT '',
			synced_at INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(store_environment, product_id)
		)`,
		`CREATE INDEX IF NOT EXISTS iap_products_product_idx
			ON iap_products(product_id, store_environment, enabled)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"subscription_period", "subscription_period TEXT NOT NULL DEFAULT ''"},
		{"last_sync_error", "last_sync_error TEXT NOT NULL DEFAULT ''"},
		{"synced_at", "synced_at INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.db.ensureColumn(ctx, "iap_products", col.name, col.def); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"app_store_price_point_id",
		"app_store_subscription_group_id",
		"app_store_locale",
		"subscription_display_name",
		"subscription_description",
		"app_store_review_note",
		"app_store_review_screenshot_url",
		"app_store_subscription_localization_id",
		"revenuecat_offering_id",
		"revenuecat_product_id",
		"app_store_connect_id",
	} {
		if err := s.db.dropColumnIfExists(ctx, "iap_products", name); err != nil {
			return err
		}
	}
	return nil
}

func (s *IAPProductStore) List(ctx context.Context, after int64, limit int) (rows []IAPProduct, nextCursor int64, err error) {
	if s == nil {
		return nil, 0, errors.New("iap product store is not configured")
	}
	if limit <= 0 {
		limit = 20
	}
	result, err := s.db.QueryContext(ctx, `SELECT `+iapProductColumns+`
		FROM iap_products
		WHERE (? = 0 OR id > ?)
		ORDER BY id ASC
		LIMIT ?`, after, after, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer result.Close()
	for result.Next() {
		p, err := scanIAPProduct(result)
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

func (s *IAPProductStore) EnabledForTopup(ctx context.Context) ([]IAPProduct, error) {
	if s == nil {
		return nil, errors.New("iap product store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+iapProductColumns+`
		FROM iap_products
		WHERE enabled = 1 AND points_grant > 0
		ORDER BY store_environment ASC, product_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IAPProduct
	for rows.Next() {
		p, err := scanIAPProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *IAPProductStore) FindEnabled(ctx context.Context, productID, storeEnvironment string) (*IAPProduct, bool, error) {
	if s == nil {
		return nil, false, errors.New("iap product store is not configured")
	}
	productID = strings.TrimSpace(productID)
	storeEnvironment = normalizeIAPStoreEnvironment(storeEnvironment)
	if productID == "" {
		return nil, false, nil
	}
	query := `SELECT ` + iapProductColumns + ` FROM iap_products
		WHERE product_id = ? AND enabled = 1 AND points_grant > 0`
	args := []any{productID}
	if storeEnvironment != "" {
		query += ` AND store_environment = ?`
		args = append(args, storeEnvironment)
	}
	query += ` ORDER BY id ASC LIMIT 2`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var matches []IAPProduct
	for rows.Next() {
		p, err := scanIAPProduct(rows)
		if err != nil {
			return nil, false, err
		}
		matches = append(matches, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(matches) != 1 {
		return nil, false, nil
	}
	return &matches[0], true, nil
}

func (s *IAPProductStore) Get(ctx context.Context, id int64) (*IAPProduct, error) {
	if s == nil {
		return nil, errors.New("iap product store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+iapProductColumns+` FROM iap_products WHERE id = ?`, id)
	p, err := scanIAPProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *IAPProductStore) Create(ctx context.Context, p *IAPProduct) error {
	if s == nil {
		return errors.New("iap product store is not configured")
	}
	return s.create(ctx, s.db, p)
}

func (s *IAPProductStore) create(ctx context.Context, exec iapProductExecutor, p *IAPProduct) error {
	if exec == nil {
		return errors.New("iap product store executor is not configured")
	}
	if err := normalizeIAPProduct(p); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := exec.ExecContext(ctx, `INSERT INTO iap_products
		(product_id, store_environment, product_type, display_name, points_grant,
		 price_currency, price_minor_units, subscription_period,
		 enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ProductID, p.StoreEnvironment, p.ProductType, p.DisplayName, p.PointsGrant,
		p.PriceCurrency, p.PriceMinorUnits, p.SubscriptionPeriod,
		boolToInt(p.Enabled), now, now)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = time.UnixMilli(now)
	p.UpdatedAt = p.CreatedAt
	return nil
}

func (s *IAPProductStore) Update(ctx context.Context, id int64, p *IAPProduct) error {
	if s == nil {
		return errors.New("iap product store is not configured")
	}
	if err := normalizeIAPProduct(p); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE iap_products SET
		product_id = ?, store_environment = ?, product_type = ?, display_name = ?,
		points_grant = ?, price_currency = ?, price_minor_units = ?,
		subscription_period = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		p.ProductID, p.StoreEnvironment, p.ProductType, p.DisplayName,
		p.PointsGrant, p.PriceCurrency, p.PriceMinorUnits,
		p.SubscriptionPeriod, boolToInt(p.Enabled), now, id)
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

func (s *IAPProductStore) Delete(ctx context.Context, id int64) error {
	if s == nil {
		return errors.New("iap product store is not configured")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM iap_products WHERE id = ?`, id)
	return err
}

func (s *IAPProductStore) MarkSyncResult(ctx context.Context, id int64, result IAPProductSyncResult, syncErr error) error {
	if s == nil {
		return errors.New("iap product store is not configured")
	}
	return s.markSyncResult(ctx, s.db, id, result, syncErr)
}

func (s *IAPProductStore) markSyncResult(ctx context.Context, exec iapProductExecutor, id int64, result IAPProductSyncResult, syncErr error) error {
	if exec == nil {
		return errors.New("iap product store executor is not configured")
	}
	now := time.Now().UnixMilli()
	errText := ""
	syncedAt := now
	if syncErr != nil {
		errText = syncErr.Error()
		syncedAt = 0
	}
	_, err := exec.ExecContext(ctx, `UPDATE iap_products SET
		last_sync_error = ?, synced_at = ?, updated_at = ?
		WHERE id = ?`,
		errText, syncedAt, now, id)
	return err
}

const iapProductColumns = `id, product_id, store_environment, product_type, display_name,
	points_grant, price_currency, price_minor_units, subscription_period, last_sync_error,
	synced_at, enabled, created_at, updated_at`

type iapProductScanner interface {
	Scan(dest ...any) error
}

func scanIAPProduct(row iapProductScanner) (IAPProduct, error) {
	var p IAPProduct
	var enabled int
	var syncedAt, created, updated int64
	err := row.Scan(&p.ID, &p.ProductID, &p.StoreEnvironment, &p.ProductType, &p.DisplayName,
		&p.PointsGrant, &p.PriceCurrency, &p.PriceMinorUnits, &p.SubscriptionPeriod, &p.LastSyncError,
		&syncedAt, &enabled, &created, &updated)
	if err != nil {
		return p, err
	}
	p.Enabled = enabled != 0
	if syncedAt > 0 {
		t := time.UnixMilli(syncedAt)
		p.SyncedAt = &t
	}
	p.CreatedAt = time.UnixMilli(created)
	p.UpdatedAt = time.UnixMilli(updated)
	return p, nil
}

func normalizeIAPProduct(p *IAPProduct) error {
	if p == nil {
		return errors.New("iap product is nil")
	}
	p.ProductID = strings.TrimSpace(p.ProductID)
	p.StoreEnvironment = normalizeIAPStoreEnvironment(p.StoreEnvironment)
	p.ProductType = normalizeIAPProductType(p.ProductType)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.PriceCurrency = strings.ToUpper(strings.TrimSpace(p.PriceCurrency))
	p.SubscriptionPeriod = normalizeIAPSubscriptionPeriod(p.SubscriptionPeriod)
	if p.ProductID == "" {
		return fmt.Errorf("product id is required")
	}
	if p.StoreEnvironment == "" {
		return fmt.Errorf("store environment is required")
	}
	if p.ProductType == "" {
		return fmt.Errorf("product type is required")
	}
	if p.PointsGrant < 0 {
		return fmt.Errorf("points grant must be non-negative")
	}
	if p.PriceMinorUnits < 0 {
		return fmt.Errorf("price must be non-negative")
	}
	return nil
}

func normalizeIAPStoreEnvironment(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "test", "testing", "sandbox", "test_store":
		return IAPStoreEnvironmentTest
	case "production", "prod", "appstore", "app_store", "store":
		return IAPStoreEnvironmentAppStore
	default:
		return ""
	}
}

func normalizeIAPProductType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "", "consumable":
		return IAPProductTypeConsumable
	case "subscription", "auto_renewable_subscription", "non_renewing_subscription":
		return IAPProductTypeSubscription
	case "non_consumable":
		return IAPProductTypeNonConsumable
	default:
		return ""
	}
}

func normalizeIAPSubscriptionPeriod(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	v = strings.ReplaceAll(v, " ", "_")
	switch v {
	case "":
		return ""
	case "WEEK", "ONE_WEEK":
		return "ONE_WEEK"
	case "MONTH", "ONE_MONTH":
		return "ONE_MONTH"
	case "TWO_MONTHS", "2_MONTHS":
		return "TWO_MONTHS"
	case "THREE_MONTHS", "3_MONTHS":
		return "THREE_MONTHS"
	case "SIX_MONTHS", "6_MONTHS":
		return "SIX_MONTHS"
	case "YEAR", "ANNUAL", "ONE_YEAR":
		return "ONE_YEAR"
	default:
		return ""
	}
}

func iapProductFromForm(data map[string]any) IAPProduct {
	return IAPProduct{
		ProductID:          stringField(data, "product_id"),
		StoreEnvironment:   stringField(data, "store_environment"),
		ProductType:        stringField(data, "product_type"),
		DisplayName:        stringField(data, "display_name"),
		PointsGrant:        int64Field(data, "points_grant"),
		PriceCurrency:      stringField(data, "price_currency"),
		PriceMinorUnits:    int64Field(data, "price_minor_units"),
		SubscriptionPeriod: stringField(data, "subscription_period"),
		Enabled:            boolField(data, "enabled", true),
	}
}

func stringField(data map[string]any, key string) string {
	v, _ := data[key].(string)
	return strings.TrimSpace(v)
}

func int64Field(data map[string]any, key string) int64 {
	switch v := data[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := strconv.ParseInt(v.String(), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func boolField(data map[string]any, key string, def bool) bool {
	v, ok := data[key]
	if !ok {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err == nil {
			return parsed
		}
	}
	return def
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
