package server

import (
	"context"
	"net/url"
	"os"
	"testing"
)

func TestDatabaseKindFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want databaseKind
	}{
		{name: "empty", want: databaseSQLite},
		{name: "postgres", url: "postgres://user:pass@example.com/db", want: databasePostgres},
		{name: "postgresql", url: "postgresql://user:pass@example.com/db", want: databasePostgres},
		{name: "postgresql raw reserved password", url: "postgresql://user:p!a|ss;word@example.com/db", want: databasePostgres},
		{name: "turso", url: "libsql://debate-bot.turso.io", want: databaseTurso},
		{name: "sqlite", url: "sqlite:///tmp/test.db", want: databaseSQLite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := databaseKindFromURL(tt.url)
			if err != nil {
				t.Fatalf("databaseKindFromURL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizePostgresURLUserInfo(t *testing.T) {
	raw := "postgresql://user:p!a|ss;word@example.com/db"
	normalized := normalizePostgresURLUserInfo(raw)
	if normalized == raw {
		t.Fatalf("normalizePostgresURLUserInfo did not encode reserved password characters")
	}
	u, err := url.Parse(normalized)
	if err != nil {
		t.Fatalf("normalized URL did not parse: %v", err)
	}
	password, ok := u.User.Password()
	if !ok {
		t.Fatal("normalized URL missing password")
	}
	if password != "p!a|ss;word" {
		t.Fatalf("password = %q, want %q", password, "p!a|ss;word")
	}
}

func TestRebindSQLForPostgres(t *testing.T) {
	got := rebindSQL(databasePostgres, `SELECT '?' AS literal, a FROM t WHERE x = ? AND y = ?`)
	want := `SELECT '?' AS literal, a FROM t WHERE x = $1 AND y = $2`
	if got != want {
		t.Fatalf("rebindSQL = %q, want %q", got, want)
	}
}

func TestPrepareSQLForPostgres(t *testing.T) {
	got := prepareSQL(databasePostgres, `id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT`)
	want := `id BIGSERIAL PRIMARY KEY, name TEXT`
	if got != want {
		t.Fatalf("prepareSQL = %q, want %q", got, want)
	}
}

func TestSQLBoolIntScansBoolAndInt(t *testing.T) {
	var fromBool sqlBoolInt
	if err := fromBool.Scan(true); err != nil {
		t.Fatalf("scan bool: %v", err)
	}
	if fromBool.Int() != 1 {
		t.Fatalf("bool scan = %d, want 1", fromBool.Int())
	}
	var fromInt sqlBoolInt
	if err := fromInt.Scan(int64(0)); err != nil {
		t.Fatalf("scan int: %v", err)
	}
	if fromInt.Int() != 0 {
		t.Fatalf("int scan = %d, want 0", fromInt.Int())
	}
}

func TestPostgresMetadataStoresIntegration(t *testing.T) {
	databaseURL := os.Getenv("POSTGRES_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("POSTGRES_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	jobs, err := NewJobRegistry("", databaseURL, "")
	if err != nil {
		t.Fatalf("NewJobRegistry postgres: %v", err)
	}
	if raw, err := jobs.db.DB(); err == nil {
		defer raw.Close()
	}
	discussions, err := NewDiscussionStore("", databaseURL, "")
	if err != nil {
		t.Fatalf("NewDiscussionStore postgres: %v", err)
	}
	defer discussions.Close()

	jobID := "pg-test-" + newJobID()
	if job := jobs.Add(jobID); job == nil || job.ID != jobID {
		t.Fatalf("jobs.Add returned %#v, want id %q", job, jobID)
	}
	owner := "pg-owner-" + newJobID()
	discussion, err := discussions.CreatePlaceholder(ctx, owner, "postgres smoke", "en-US", "default")
	if err != nil {
		t.Fatalf("CreatePlaceholder postgres: %v", err)
	}
	if discussion == nil || discussion.ID == "" {
		t.Fatalf("CreatePlaceholder returned %#v", discussion)
	}
	if _, err := discussions.SetJob(ctx, owner, discussion.ID, jobID); err != nil {
		t.Fatalf("SetJob postgres: %v", err)
	}
	loaded, err := discussions.GetByJobID(ctx, jobID)
	if err != nil {
		t.Fatalf("GetByJobID postgres: %v", err)
	}
	if loaded == nil || loaded.ID != discussion.ID {
		t.Fatalf("GetByJobID returned %#v, want discussion %q", loaded, discussion.ID)
	}
}
