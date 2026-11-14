// Package testdb gives tests a fresh, migrated Workbench schema.
//
// Tests that need it skip unless WORKBENCH_TEST_DATABASE_URL points at a
// PostgreSQL database the test may create schemas in.
package testdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kagent-dev/kagent/go/core/pkg/migrations"

	wbmigrations "github.com/thehaymaker/kagent-porcelain/server/migrations"
	"github.com/thehaymaker/kagent-porcelain/server/internal/store"
)

// EnvVar names the database URL tests use.
const EnvVar = "WORKBENCH_TEST_DATABASE_URL"

// New migrates a schema unique to this test, through kagent's migration
// runner exactly as the server does, and returns a store on it.
func New(t testing.TB) *store.Store {
	t.Helper()
	url := os.Getenv(EnvVar)
	if url == "" {
		t.Skipf("%s not set; skipping database test", EnvVar)
	}
	ctx := context.Background()
	schema := "wbtest_" + strings.ToLower(strings.TrimPrefix(store.NewID("s"), "s_"))
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	src := migrations.Source{Name: "workbench", Schema: schema, TrackingTable: wbmigrations.TrackingTable, FS: wbmigrations.FS, Dir: "."}
	if err := migrations.RunUp(ctx, cfg, []migrations.Source{src}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if conn, err := pgx.Connect(ctx, url); err == nil {
			_, _ = conn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE", pgx.Identifier{schema}.Sanitize()))
			_ = conn.Close(ctx)
		}
	})
	return store.New(pool)
}
