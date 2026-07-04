package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	libsql "github.com/tursodatabase/libsql-client-go/libsql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type databaseKind string

const (
	databaseSQLite   databaseKind = "sqlite"
	databaseTurso    databaseKind = "turso"
	databasePostgres databaseKind = "postgres"
)

type sqlDB struct {
	raw  *sql.DB
	kind databaseKind
}

type sqlTx struct {
	raw  *sql.Tx
	kind databaseKind
}

func databaseKindFromURL(databaseURL string) (databaseKind, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return databaseSQLite, nil
	}
	databaseURL = normalizePostgresURLUserInfo(databaseURL)
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
		return databasePostgres, nil
	case "libsql", "turso":
		return databaseTurso, nil
	case "sqlite", "file":
		return databaseSQLite, nil
	default:
		return "", fmt.Errorf("unsupported database URL scheme %q", u.Scheme)
	}
}

func openSQLDatabase(dbPath, databaseURL, authToken string) (*sqlDB, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	kind, err := databaseKindFromURL(databaseURL)
	if err != nil {
		return nil, err
	}
	var raw *sql.DB
	switch kind {
	case databasePostgres:
		databaseURL = normalizePostgresURLUserInfo(databaseURL)
		raw, err = sql.Open("pgx", databaseURL)
	case databaseTurso:
		var opts []libsql.Option
		if authToken != "" {
			opts = append(opts, libsql.WithAuthToken(authToken))
		}
		c, cerr := libsql.NewConnector(databaseURL, opts...)
		if cerr != nil {
			return nil, cerr
		}
		raw = sql.OpenDB(c)
	case databaseSQLite:
		path := dbPath
		if strings.TrimSpace(databaseURL) != "" {
			if u, uerr := url.Parse(databaseURL); uerr == nil && u.Scheme == "sqlite" {
				path = strings.TrimPrefix(u.Path, "/")
				if u.Host != "" {
					path = filepath.Join(u.Host, path)
				}
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		raw, err = sql.Open("sqlite3", sqliteDSN(path))
	}
	if err != nil {
		return nil, err
	}
	tuneSQLDB(raw, kind)
	return &sqlDB{raw: raw, kind: kind}, nil
}

func openGormDatabase(dbPath, databaseURL, authToken string, log gormlogger.Interface) (*gorm.DB, databaseKind, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	kind, err := databaseKindFromURL(databaseURL)
	if err != nil {
		return nil, "", err
	}
	var db *gorm.DB
	switch kind {
	case databasePostgres:
		databaseURL = normalizePostgresURLUserInfo(databaseURL)
		db, err = gorm.Open(postgres.Open(databaseURL), &gorm.Config{Logger: log})
	case databaseTurso:
		handle, herr := openSQLDatabase(dbPath, databaseURL, authToken)
		if herr != nil {
			return nil, "", herr
		}
		db, err = gorm.Open(sqlite.New(sqlite.Config{Conn: handle.raw}), &gorm.Config{Logger: log})
	case databaseSQLite:
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, "", err
		}
		db, err = gorm.Open(sqlite.Open(sqliteDSN(dbPath)), &gorm.Config{Logger: log})
	}
	if err != nil {
		return nil, "", err
	}
	if raw, derr := db.DB(); derr == nil {
		tuneSQLDB(raw, kind)
	}
	return db, kind, nil
}

func normalizePostgresURLUserInfo(databaseURL string) string {
	if !strings.HasPrefix(databaseURL, "postgres://") && !strings.HasPrefix(databaseURL, "postgresql://") {
		return databaseURL
	}
	if _, err := url.Parse(databaseURL); err == nil {
		return databaseURL
	}
	scheme, rest, ok := strings.Cut(databaseURL, "://")
	if !ok {
		return databaseURL
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return databaseURL
	}
	userInfo := rest[:at]
	hostAndPath := rest[at+1:]
	user, password, ok := strings.Cut(userInfo, ":")
	if !ok {
		return databaseURL
	}
	return scheme + "://" + url.UserPassword(user, password).String() + "@" + hostAndPath
}

func tuneSQLDB(db *sql.DB, kind databaseKind) {
	if db == nil {
		return
	}
	switch kind {
	case databasePostgres:
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
	default:
		db.SetMaxOpenConns(1)
		db.SetConnMaxIdleTime(30 * time.Second)
		db.SetConnMaxLifetime(5 * time.Minute)
	}
}

func (db *sqlDB) Close() error {
	if db == nil || db.raw == nil {
		return nil
	}
	return db.raw.Close()
}

func (db *sqlDB) PingContext(ctx context.Context) error {
	if db == nil || db.raw == nil {
		return nil
	}
	return db.raw.PingContext(ctx)
}

func (db *sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.raw.ExecContext(ctx, rebindSQL(db.kind, prepareSQL(db.kind, query)), args...)
}

func (db *sqlDB) Exec(query string, args ...any) (sql.Result, error) {
	return db.ExecContext(context.Background(), query, args...)
}

func (db *sqlDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.raw.QueryContext(ctx, rebindSQL(db.kind, query), args...)
}

func (db *sqlDB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(context.Background(), query, args...)
}

func (db *sqlDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.raw.QueryRowContext(ctx, rebindSQL(db.kind, query), args...)
}

func (db *sqlDB) QueryRow(query string, args ...any) *sql.Row {
	return db.QueryRowContext(context.Background(), query, args...)
}

func (db *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sqlTx, error) {
	tx, err := db.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlTx{raw: tx, kind: db.kind}, nil
}

func (tx *sqlTx) Commit() error {
	if tx == nil || tx.raw == nil {
		return nil
	}
	return tx.raw.Commit()
}

func (tx *sqlTx) Rollback() error {
	if tx == nil || tx.raw == nil {
		return nil
	}
	return tx.raw.Rollback()
}

func (tx *sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.raw.ExecContext(ctx, rebindSQL(tx.kind, prepareSQL(tx.kind, query)), args...)
}

func (tx *sqlTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.raw.QueryContext(ctx, rebindSQL(tx.kind, query), args...)
}

func (tx *sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.raw.QueryRowContext(ctx, rebindSQL(tx.kind, query), args...)
}

func (db *sqlDB) tableExists(ctx context.Context, name string) bool {
	if db == nil || db.raw == nil || strings.TrimSpace(name) == "" {
		return false
	}
	var count int
	var err error
	if db.kind == databasePostgres {
		err = db.QueryRowContext(ctx, `SELECT COUNT(1)
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?`, name).Scan(&count)
	} else {
		err = db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
	}
	return err == nil && count > 0
}

func (db *sqlDB) ensureColumn(ctx context.Context, table, column, definition string) error {
	if err := validateSQLIdentifier(table); err != nil {
		return err
	}
	if err := validateSQLIdentifier(column); err != nil {
		return err
	}
	if db.kind == databasePostgres {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1)
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`, table, column).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		_, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, definition))
		return err
	}
	rows, err := db.raw.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultVal any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.raw.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, definition))
	return err
}

func validateSQLIdentifier(s string) error {
	if s == "" {
		return errors.New("empty SQL identifier")
	}
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("unsafe SQL identifier %q", s)
	}
	return nil
}

func prepareSQL(kind databaseKind, query string) string {
	if kind != databasePostgres {
		return query
	}
	query = strings.ReplaceAll(query, "INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY")
	return replaceStandaloneSQLType(query, "INTEGER", "BIGINT")
}

func replaceStandaloneSQLType(query, from, to string) string {
	if query == "" || from == "" || !strings.Contains(strings.ToUpper(query), strings.ToUpper(from)) {
		return query
	}
	var b strings.Builder
	b.Grow(len(query))
	inSingle := false
	inDouble := false
	for i := 0; i < len(query); {
		ch := query[i]
		if ch == '\'' && !inDouble {
			b.WriteByte(ch)
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				i += 2
				b.WriteByte(query[i-1])
				continue
			}
			inSingle = !inSingle
			i++
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			b.WriteByte(ch)
			i++
			continue
		}
		if !inSingle && !inDouble && i+len(from) <= len(query) &&
			strings.EqualFold(query[i:i+len(from)], from) &&
			(i == 0 || !isSQLIdentByte(query[i-1])) &&
			(i+len(from) == len(query) || !isSQLIdentByte(query[i+len(from)])) {
			b.WriteString(to)
			i += len(from)
			continue
		}
		b.WriteByte(ch)
		i++
	}
	return b.String()
}

func isSQLIdentByte(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}

func rebindSQL(kind databaseKind, query string) string {
	if kind != databasePostgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	inDouble := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' && !inDouble {
			b.WriteByte(ch)
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				i++
				b.WriteByte(query[i])
				continue
			}
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			b.WriteByte(ch)
			continue
		}
		if ch == '?' && !inSingle && !inDouble {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(arg))
			arg++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

type sqlBoolInt struct {
	v int
}

func (b *sqlBoolInt) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		b.v = 0
	case bool:
		if v {
			b.v = 1
		} else {
			b.v = 0
		}
	case int64:
		b.v = int(v)
	case int:
		b.v = v
	case []byte:
		return b.Scan(string(v))
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "t", "true", "y", "yes":
			b.v = 1
		case "0", "f", "false", "n", "no", "":
			b.v = 0
		default:
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			b.v = n
		}
	default:
		return fmt.Errorf("unsupported bool-int scan type %T", src)
	}
	return nil
}

func (b sqlBoolInt) Int() int { return b.v }
