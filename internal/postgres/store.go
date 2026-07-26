package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/jahwag/zestmem/migrations"
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
	if err := migrations.Apply(ctx, pool); err != nil {
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
		"SELECT id::text, type::text, display_name FROM principals WHERE external_issuer=$1 AND external_subject=$2 AND disabled_at IS NULL",
		issuer, subject,
	).Scan(&principal.ID, &principal.Type, &principal.DisplayName)
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
		"SELECT p.id::text, p.type::text, p.display_name, k.id::text FROM api_keys k JOIN principals p ON p.id=k.principal_id "+
			"WHERE k.digest=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND p.disabled_at IS NULL",
		digest,
	).Scan(&principal.ID, &principal.Type, &principal.DisplayName, &keyID)
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
		err := s.pool.QueryRow(ctx, `SELECT can_read_space($1,$2)`, principal.ID, spaceID).Scan(&allowed)
		if err != nil {
			return err
		}
	} else {
		minimum := "contributor"
		if action == memory.ActionRetract {
			minimum = "maintainer"
		}
		err := s.pool.QueryRow(ctx, `SELECT has_direct_space_role($1,$2,$3::space_role)`,
			principal.ID, spaceID, minimum).Scan(&allowed)
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
	rows, err := s.pool.Query(ctx, `SELECT space_id::text FROM readable_space_ids($1) ORDER BY space_id::text`, principal.ID)
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
