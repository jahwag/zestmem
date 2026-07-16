package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/bytelope/zestmem/internal/memory"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) ByExternalIdentity(ctx context.Context, issuer, subject string) (memory.Principal, error) {
	var principal memory.Principal
	err := s.pool.QueryRow(ctx,
		"SELECT id::text, type::text FROM principals WHERE external_issuer=$1 AND external_subject=$2 AND disabled_at IS NULL",
		issuer, subject,
	).Scan(&principal.ID, &principal.Type)
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Principal{}, memory.ErrNotFound
	}
	if err == nil {
		_, err = s.pool.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id) VALUES($1,'authenticate_oidc','principal',$2)`, principal.ID, principal.ID)
	}
	return principal, err
}

func (s *Store) ByAPIKeyDigest(ctx context.Context, digest []byte) (memory.Principal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return memory.Principal{}, err
	}
	defer tx.Rollback(ctx)
	var principal memory.Principal
	var keyID string
	err = tx.QueryRow(ctx,
		"SELECT p.id::text, p.type::text, k.id::text FROM api_keys k JOIN principals p ON p.id=k.principal_id "+
			"WHERE k.digest=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND p.disabled_at IS NULL",
		digest,
	).Scan(&principal.ID, &principal.Type, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Principal{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Principal{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE api_keys SET last_used_at=$1 WHERE id=$2", time.Now().UTC(), keyID); err != nil {
		return memory.Principal{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,metadata) VALUES($1,'authenticate_api_key','principal',$2,jsonb_build_object('key_id',$3::text))`, principal.ID, principal.ID, keyID); err != nil {
		return memory.Principal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Principal{}, err
	}
	return principal, nil
}

func (s *Store) Require(ctx context.Context, principal memory.Principal, spaceID string, action memory.Action) error {
	var allowed bool
	if action == memory.ActionRead {
		err := s.pool.QueryRow(ctx,
			"WITH RECURSIVE readable(id) AS ("+
				" SELECT g.space_id FROM space_grants g JOIN spaces s ON s.id=g.space_id WHERE g.principal_id=$1 AND s.suspended_at IS NULL"+
				" UNION SELECT child.id FROM spaces child JOIN readable parent ON child.parent_id=parent.id WHERE child.suspended_at IS NULL"+
				") SELECT EXISTS(SELECT 1 FROM readable WHERE id=$2::uuid)",
			principal.ID, spaceID,
		).Scan(&allowed)
		if err != nil {
			return err
		}
	} else {
		minimum := "contributor"
		if action == memory.ActionRetract {
			minimum = "maintainer"
		}
		err := s.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM space_grants g JOIN spaces s ON s.id=g.space_id WHERE g.principal_id=$1 AND g.space_id=$2 AND s.suspended_at IS NULL AND "+
				"CASE role WHEN 'reader' THEN 1 WHEN 'contributor' THEN 2 WHEN 'maintainer' THEN 3 WHEN 'owner' THEN 4 END >= "+
				"CASE $3 WHEN 'reader' THEN 1 WHEN 'contributor' THEN 2 WHEN 'maintainer' THEN 3 WHEN 'owner' THEN 4 END)",
			principal.ID, spaceID, minimum,
		).Scan(&allowed)
		if err != nil {
			return err
		}
	}
	if !allowed {
		return memory.ErrForbidden
	}
	return nil
}

func (s *Store) ReadableSpaces(ctx context.Context, principal memory.Principal) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		"WITH RECURSIVE readable(id) AS ("+
			" SELECT g.space_id FROM space_grants g JOIN spaces s ON s.id=g.space_id WHERE g.principal_id=$1 AND s.suspended_at IS NULL"+
			" UNION SELECT child.id FROM spaces child JOIN readable parent ON child.parent_id=parent.id WHERE child.suspended_at IS NULL"+
			") SELECT DISTINCT id::text FROM readable ORDER BY id::text",
		principal.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var spaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		spaces = append(spaces, id)
	}
	return spaces, rows.Err()
}
