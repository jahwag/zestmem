// Package migrations owns ZestMem schema discovery and upgrades.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

type migration struct {
	version  int
	name     string
	body     string
	checksum string
}

type appliedMigration struct {
	name     string
	checksum string
}

var legacyTables = []string{
	"principals",
	"api_keys",
	"spaces",
	"space_grants",
	"workspace_bindings",
	"memories",
	"memory_revisions",
	"memory_embeddings",
	"relations",
	"idempotency_records",
	"audit_events",
}

// Apply serializes, verifies, and applies all embedded forward migrations.
// A complete schema created from 001_init.sql before the runner existed is
// adopted as version 1; partial legacy schemas are rejected.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := load()
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('zestmem-schema-migrations'))`); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
 version integer PRIMARY KEY,
 name text NOT NULL,
 checksum text NOT NULL CHECK (length(checksum)=64),
 applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := readApplied(ctx, tx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		if err := adoptLegacySchema(ctx, tx, migrations[0], applied); err != nil {
			return err
		}
	}
	if err := verifyApplied(applied, migrations); err != nil {
		return err
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, migration.body); err != nil {
			return fmt.Errorf("apply migration %03d_%s: %w", migration.version, migration.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`,
			migration.version, migration.name, migration.checksum); err != nil {
			return fmt.Errorf("record migration %03d_%s: %w", migration.version, migration.name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	return nil
}

func load() ([]migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		underscore := strings.IndexByte(name, '_')
		if underscore < 1 {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.Atoi(name[:underscore])
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version in %q", name)
		}
		migrationName := strings.TrimSuffix(name[underscore+1:], ".sql")
		if migrationName == "" || strings.Contains(migrationName, ".") {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, migration{
			version: version, name: migrationName, body: string(body), checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	if len(migrations) == 0 {
		return nil, errors.New("no embedded migrations")
	}
	for i, migration := range migrations {
		want := i + 1
		if migration.version != want {
			return nil, fmt.Errorf("migration sequence has version %d, want %d", migration.version, want)
		}
	}
	return migrations, nil
}

func readApplied(ctx context.Context, tx pgx.Tx) (map[int]appliedMigration, error) {
	rows, err := tx.Query(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read schema migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var item appliedMigration
		if err := rows.Scan(&version, &item.name, &item.checksum); err != nil {
			return nil, fmt.Errorf("scan schema migration: %w", err)
		}
		applied[version] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema migrations: %w", err)
	}
	return applied, nil
}

func adoptLegacySchema(ctx context.Context, tx pgx.Tx, initial migration, applied map[int]appliedMigration) error {
	var present int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[]) name
 WHERE to_regclass('public.' || name) IS NOT NULL`, legacyTables).Scan(&present); err != nil {
		return fmt.Errorf("inspect legacy schema: %w", err)
	}
	if present == 0 {
		return nil
	}
	if present != len(legacyTables) {
		return fmt.Errorf("refusing to adopt partial legacy schema: found %d of %d M1 tables", present, len(legacyTables))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES($1,$2,$3)`,
		initial.version, initial.name, initial.checksum); err != nil {
		return fmt.Errorf("adopt legacy schema: %w", err)
	}
	applied[initial.version] = appliedMigration{name: initial.name, checksum: initial.checksum}
	return nil
}

func verifyApplied(applied map[int]appliedMigration, migrations []migration) error {
	embedded := make(map[int]migration, len(migrations))
	for _, migration := range migrations {
		embedded[migration.version] = migration
	}
	for version, item := range applied {
		migration, ok := embedded[version]
		if !ok {
			return fmt.Errorf("database migration %d is newer than this binary", version)
		}
		if item.name != migration.name || item.checksum != migration.checksum {
			return fmt.Errorf("migration %03d_%s differs from the applied schema", version, migration.name)
		}
	}
	return nil
}
