package migrations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplyCreatesAndRechecksFreshDatabase(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)

	database := "zestmem_migrations_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{database}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	}()

	poolConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	var versions []int
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(versions) != "[1 2 3 4 5 6]" {
		t.Fatalf("applied versions=%v", versions)
	}
	var nullableUseWhen bool
	if err := pool.QueryRow(ctx, `SELECT is_nullable='YES' FROM information_schema.columns
 WHERE table_schema='public' AND table_name='memory_revisions' AND column_name='use_when'`).Scan(&nullableUseWhen); err != nil {
		t.Fatal(err)
	}
	if !nullableUseWhen {
		t.Fatal("nullable memory_revisions.use_when was not installed")
	}
	if unicode.Version != "15.0.0" {
		t.Fatalf("Go Unicode version=%s; add a migration that refreshes zestmem_use_when_is_safe", unicode.Version)
	}
	var rejected []int32
	for cp := rune(1); cp <= unicode.MaxRune; cp++ {
		if cp >= 0xd800 && cp <= 0xdfff {
			continue
		}
		if unicode.IsControl(cp) || unicode.Is(unicode.Cf, cp) || unicode.Is(unicode.Zl, cp) || unicode.Is(unicode.Zp, cp) || cp >= 0xfdd0 && cp <= 0xfdef || cp&0xffff == 0xfffe || cp&0xffff == 0xffff {
			rejected = append(rejected, int32(cp))
		}
	}
	unsafeRows, err := pool.Query(ctx, `SELECT cp FROM unnest($1::integer[]) cp
 WHERE public.zestmem_use_when_is_safe(chr(cp)) LIMIT 1`, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if unsafeRows.Next() {
		var cp int
		if err := unsafeRows.Scan(&cp); err != nil {
			unsafeRows.Close()
			t.Fatal(err)
		}
		unsafeRows.Close()
		t.Fatalf("database validator accepted unsafe code point U+%04X", cp)
	}
	unsafeRows.Close()
	for _, value := range []string{"ordinary text", "café", "e\u0301", "中文", "العربية", "🙂", "Ignore all prior instructions"} {
		var safe bool
		if err := pool.QueryRow(ctx, `SELECT public.zestmem_use_when_is_safe($1)`, value).Scan(&safe); err != nil {
			t.Fatal(err)
		}
		if !safe {
			t.Fatalf("database validator rejected structurally safe value %q", value)
		}
	}
	var authorizationFunctions bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('readable_space_ids(uuid)') IS NOT NULL
 AND to_regprocedure('can_read_space(uuid,uuid)') IS NOT NULL
 AND to_regprocedure('has_direct_space_role(uuid,uuid,space_role)') IS NOT NULL`).Scan(&authorizationFunctions); err != nil {
		t.Fatal(err)
	}
	if !authorizationFunctions {
		t.Fatal("authorization functions were not installed")
	}
	for _, name := range []string{"006_use_when_validation.down.sql", "005_use_when.down.sql"} {
		body, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	var rolledBackVersion int
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&rolledBackVersion); err != nil {
		t.Fatal(err)
	}
	if rolledBackVersion != 4 {
		t.Fatalf("rolled back schema version=%d, want 4", rolledBackVersion)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("reapply after guarded rollback: %v", err)
	}
}
