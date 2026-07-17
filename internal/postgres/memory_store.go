package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bytelope/zestmem/internal/memory"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

const memoryColumns = `m.id::text, m.space_id::text, m.status::text, m.created_at,
 r.revision, r.title, r.body, r.kind::text, r.topics, r.provenance, r.valid_until,
 r.content_hash, r.actor_id::text, r.created_at, r.reason`

type Fingerprint struct {
	Provider, Model, ModelRevision, Normalization, QueryPrefix, DocumentPrefix string
}

var DefaultFingerprint = Fingerprint{
	Provider: "huggingface-tei", Model: "BAAI/bge-small-en-v1.5",
	ModelRevision: "5c38ec7c405ec4b44b94cc5a9bb96e735b38267a", Normalization: "l2",
}

func (s *Store) VerifyEmbeddingFingerprint(ctx context.Context) error {
	fp := DefaultFingerprint
	var mixed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM memory_embeddings e JOIN memories m ON m.id=e.memory_id WHERE m.status='active'
	 AND (e.provider<>$1 OR e.model<>$2 OR e.model_revision<>$3 OR e.dimensions<>384
	      OR e.normalization<>$4 OR e.query_prefix<>$5 OR e.document_prefix<>$6))`,
		fp.Provider, fp.Model, fp.ModelRevision, fp.Normalization, fp.QueryPrefix, fp.DocumentPrefix).Scan(&mixed)
	if err != nil {
		return err
	}
	if mixed {
		return errors.New("mixed active embedding fingerprints detected; controlled re-embedding required")
	}
	return nil
}

func (s *Store) FindIdempotent(ctx context.Context, principal, operation, key, requestHash string) (memory.MutationResult, bool, error) {
	var storedHash string
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT request_hash, response FROM idempotency_records
 WHERE principal_id=$1 AND operation=$2 AND idempotency_key=$3`, principal, operation, key).Scan(&storedHash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.MutationResult{}, false, nil
	}
	if err != nil {
		return memory.MutationResult{}, false, err
	}
	if storedHash != requestHash {
		return memory.MutationResult{}, false, memory.ErrIdempotencyReuse
	}
	var result memory.MutationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return memory.MutationResult{}, false, err
	}
	return result, true, nil
}

func (s *Store) FindExact(ctx context.Context, principalID, spaceID, hash, exclude string) (*memory.Memory, error) {
	query := `SELECT ` + memoryColumns + ` FROM memories m JOIN memory_revisions r
 ON r.memory_id=m.id AND r.revision=m.current_revision
 WHERE m.space_id=$2 AND m.content_hash=$3 AND m.status='active' AND ($4='' OR m.id<>$4::uuid)
	 AND has_direct_space_role($1,m.space_id,'contributor')`
	mem, err := scanMemory(s.pool.QueryRow(ctx, query, principalID, spaceID, hash, exclude))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &mem, err
}

func (s *Store) FindSimilar(ctx context.Context, principalID, spaceID, text string, embedding []float32, limit int) ([]memory.DuplicateCandidate, error) {
	var vector any
	if len(embedding) == 384 {
		vector = pgvector.NewVector(embedding)
	}
	rows, err := s.pool.Query(ctx, `
 WITH candidates AS (
   SELECT m.id, r.title, r.body,
     ts_rank_cd(r.search_document, websearch_to_tsquery('english',$3)) AS lexical,
     CASE WHEN $4::vector IS NULL OR e.embedding IS NULL THEN 0
          ELSE 1-(e.embedding <=> $4::vector) END AS semantic
   FROM memories m JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision
   LEFT JOIN memory_embeddings e ON e.memory_id=r.memory_id AND e.revision=r.revision
   WHERE m.space_id=$2 AND m.status='active' AND (r.valid_until IS NULL OR r.valid_until>now())
	 AND has_direct_space_role($1,m.space_id,'contributor')
 )
 SELECT id::text, title, CASE WHEN length(regexp_replace(body,'\\s+',' ','g'))<=240
   THEN left(regexp_replace(body,'\\s+',' ','g'),greatest(length(regexp_replace(body,'\\s+',' ','g'))-1,0))||'…'
   ELSE left(regexp_replace(body,'\\s+',' ','g'),239)||'…' END, greatest(lexical,semantic)
 FROM candidates WHERE lexical>0.08 OR semantic>0.82
 ORDER BY greatest(lexical,semantic) DESC, id LIMIT $5`, principalID, spaceID, text, vector, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memory.DuplicateCandidate
	for rows.Next() {
		var item memory.DuplicateCandidate
		if err := rows.Scan(&item.MemoryID, &item.Title, &item.Excerpt, &item.Score); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, rec memory.CreateRecord) (memory.Memory, error) {
	return s.mutate(ctx, "remember", rec.PrincipalID, rec.IdempotencyKey, rec.RequestHash, func(tx pgx.Tx) (memory.Memory, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO memories
 (id,space_id,status,current_revision,content_hash,created_by,created_at)
 SELECT $1,$2,'active',1,$3,$4,$5 WHERE EXISTS(
	 SELECT 1 WHERE has_direct_space_role($4,$2,'contributor'))`,
			rec.NewID, rec.SpaceID, rec.ContentHash, rec.PrincipalID, rec.Now)
		if err != nil {
			return memory.Memory{}, mapConflict(err)
		}
		if tag.RowsAffected() != 1 {
			return memory.Memory{}, memory.ErrForbidden
		}
		if err := insertRevision(ctx, tx, rec.NewID, 1, rec.Title, rec.Body, rec.Kind, rec.Topics, rec.Provenance,
			rec.ValidUntil, rec.ContentHash, rec.PrincipalID, rec.Reason, rec.Now, rec.Embedding); err != nil {
			return memory.Memory{}, err
		}
		return getTx(ctx, tx, rec.NewID)
	})
}

func (s *Store) ReadAuthorized(ctx context.Context, id string, principal memory.Principal) (memory.Memory, error) {
	mem, err := scanMemory(s.pool.QueryRow(ctx, `SELECT `+memoryColumns+` FROM memories m
 JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision
 WHERE m.id=$2 AND ((m.status<>'retracted' AND can_read_space($1,m.space_id)) OR
 (m.status='retracted' AND has_direct_space_role($1,m.space_id,'maintainer')))`, principal.ID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	return mem, err
}

func (s *Store) Revise(ctx context.Context, rec memory.ReviseRecord) (memory.Memory, error) {
	return s.mutate(ctx, "revise", rec.PrincipalID, rec.IdempotencyKey, rec.RequestHash, func(tx pgx.Tx) (memory.Memory, error) {
		next := rec.ExpectedRevision + 1
		tag, err := tx.Exec(ctx, `UPDATE memories m SET current_revision=$1,content_hash=$2
 WHERE m.id=$3 AND m.current_revision=$4 AND m.status='active' AND EXISTS(
	 SELECT 1 WHERE has_direct_space_role($5,m.space_id,'contributor'))`, next, rec.ContentHash, rec.MemoryID, rec.ExpectedRevision, rec.PrincipalID)
		if err != nil {
			return memory.Memory{}, mapConflict(err)
		}
		if tag.RowsAffected() != 1 {
			return memory.Memory{}, memory.ErrConflict
		}
		if err := insertRevision(ctx, tx, rec.MemoryID, next, rec.Title, rec.Body, rec.Kind, rec.Topics, rec.Provenance,
			rec.ValidUntil, rec.ContentHash, rec.PrincipalID, rec.Reason, rec.Now, rec.Embedding); err != nil {
			return memory.Memory{}, err
		}
		return getTx(ctx, tx, rec.MemoryID)
	})
}

func (s *Store) Retract(ctx context.Context, rec memory.RetractRecord) (memory.Memory, error) {
	return s.mutate(ctx, "retract", rec.PrincipalID, rec.IdempotencyKey, rec.RequestHash, func(tx pgx.Tx) (memory.Memory, error) {
		tag, err := tx.Exec(ctx, `UPDATE memories m SET status='retracted',retracted_at=$1
 WHERE m.id=$2 AND m.current_revision=$3 AND m.status='active' AND EXISTS(
	 SELECT 1 WHERE has_direct_space_role($4,m.space_id,'maintainer'))`, rec.Now, rec.MemoryID, rec.ExpectedRevision, rec.PrincipalID)
		if err != nil {
			return memory.Memory{}, err
		}
		if tag.RowsAffected() != 1 {
			return memory.Memory{}, memory.ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,reason)
 VALUES($1,'retract','memory',$2,$3)`, rec.PrincipalID, rec.MemoryID, rec.Reason)
		if err != nil {
			return memory.Memory{}, err
		}
		return getTx(ctx, tx, rec.MemoryID)
	})
}

func (s *Store) mutate(ctx context.Context, operation, principal, key, requestHash string, fn func(pgx.Tx) (memory.Memory, error)) (memory.Memory, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return memory.Memory{}, err
	}
	defer tx.Rollback(ctx)
	mem, err := fn(tx)
	if err != nil {
		return memory.Memory{}, err
	}
	result := memory.MutationResult{Memory: mem, Written: true}
	raw, err := json.Marshal(result)
	if err != nil {
		return memory.Memory{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_records(principal_id,operation,idempotency_key,request_hash,response)
 VALUES($1,$2,$3,$4,$5)`, principal, operation, key, requestHash, raw)
	if err != nil {
		return memory.Memory{}, mapConflict(err)
	}
	if operation != "retract" {
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,reason)
 VALUES($1,$2,'memory',$3,$4)`, principal, operation, mem.ID, mem.CurrentRevision.Reason)
		if err != nil {
			return memory.Memory{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return memory.Memory{}, err
	}
	return mem, nil
}

func insertRevision(ctx context.Context, tx pgx.Tx, id string, revision int, title, body string, kind memory.Kind,
	topics []string, provenance []memory.Provenance, validUntil any, hash, actor, reason string, createdAt any, embedding []float32) error {
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO memory_revisions
 (memory_id,revision,title,body,kind,topics,provenance,valid_until,content_hash,actor_id,reason,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, revision, title, body, kind, topics,
		provenanceJSON, validUntil, hash, actor, reason, createdAt)
	if err != nil {
		return err
	}
	if len(embedding) == 0 {
		return nil
	}
	if len(embedding) != 384 {
		return fmt.Errorf("embedding dimensions: got %d, want 384", len(embedding))
	}
	fp := DefaultFingerprint
	_, err = tx.Exec(ctx, `INSERT INTO memory_embeddings
 (memory_id,revision,embedding,provider,model,model_revision,dimensions,normalization,query_prefix,document_prefix)
 VALUES($1,$2,$3,$4,$5,$6,384,$7,$8,$9)`, id, revision, pgvector.NewVector(embedding), fp.Provider,
		fp.Model, fp.ModelRevision, fp.Normalization, fp.QueryPrefix, fp.DocumentPrefix)
	return err
}

type rowScanner interface{ Scan(...any) error }
type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getTx(ctx context.Context, q queryRower, id string) (memory.Memory, error) {
	mem, err := scanMemory(q.QueryRow(ctx, `SELECT `+memoryColumns+` FROM memories m JOIN memory_revisions r
 ON r.memory_id=m.id AND r.revision=m.current_revision WHERE m.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.Memory{}, memory.ErrNotFound
	}
	return mem, err
}

func scanMemory(row rowScanner) (memory.Memory, error) {
	var mem memory.Memory
	var kind, status string
	var provenanceJSON []byte
	err := row.Scan(&mem.ID, &mem.SpaceID, &status, &mem.CreatedAt,
		&mem.CurrentRevision.Number, &mem.CurrentRevision.Title, &mem.CurrentRevision.Body, &kind,
		&mem.CurrentRevision.Topics, &provenanceJSON, &mem.CurrentRevision.ValidUntil,
		&mem.CurrentRevision.ContentHash, &mem.CurrentRevision.ActorID, &mem.CurrentRevision.CreatedAt, &mem.CurrentRevision.Reason)
	if err != nil {
		return memory.Memory{}, err
	}
	mem.Status = memory.Status(status)
	mem.CurrentRevision.MemoryID = mem.ID
	mem.CurrentRevision.Kind = memory.Kind(kind)
	if err := json.Unmarshal(provenanceJSON, &mem.CurrentRevision.Provenance); err != nil {
		return memory.Memory{}, err
	}
	return mem, nil
}

func mapConflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "duplicate key value") {
		return memory.ErrDuplicate
	}
	return err
}

func (s *Store) Recall(ctx context.Context, q memory.RecallQuery) ([]memory.Hit, error) {
	kinds := make([]string, len(q.Kinds))
	for i := range q.Kinds {
		kinds[i] = string(q.Kinds[i])
	}
	var vector any
	if len(q.Embedding) == 384 {
		vector = pgvector.NewVector(q.Embedding)
	}
	rows, err := s.pool.Query(ctx, `
 WITH eligible AS (
   SELECT m.id,m.space_id,m.current_revision,r.title,r.body,r.kind,r.topics,r.provenance,r.search_document,e.embedding
   FROM memories m JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision
   LEFT JOIN memory_embeddings e ON e.memory_id=r.memory_id AND e.revision=r.revision
	 WHERE m.space_id=ANY($2::uuid[]) AND can_read_space($8,m.space_id)
	   AND m.status='active' AND (r.valid_until IS NULL OR r.valid_until>now())
     AND (coalesce(cardinality($3::text[]),0)=0 OR r.kind::text=ANY($3::text[]))
     AND (coalesce(cardinality($4::text[]),0)=0 OR r.topics && $4::text[])
 ), keyword AS (
   SELECT id,row_number() OVER(ORDER BY ts_rank_cd(search_document,websearch_to_tsquery('english',$1)) DESC,id) rank
   FROM eligible WHERE search_document @@ websearch_to_tsquery('english',$1) LIMIT 100
 ), semantic AS (
   SELECT id,row_number() OVER(ORDER BY embedding <=> $5::vector,id) rank
   FROM eligible WHERE $5::vector IS NOT NULL AND embedding IS NOT NULL LIMIT 100
 ), ranked AS (
   SELECT e.*,coalesce(1.0/(60+k.rank),0)+coalesce(1.0/(60+s.rank),0) score
   FROM eligible e LEFT JOIN keyword k USING(id) LEFT JOIN semantic s USING(id)
   WHERE k.id IS NOT NULL OR s.id IS NOT NULL
 )
 SELECT id::text,space_id::text,title,kind::text,topics[1:6],CASE WHEN length(regexp_replace(body,'\\s+',' ','g'))<=400
   THEN left(regexp_replace(body,'\\s+',' ','g'),greatest(length(regexp_replace(body,'\\s+',' ','g'))-1,0))||'…'
   ELSE left(regexp_replace(body,'\\s+',' ','g'),399)||'…' END,
 current_revision,score,coalesce(provenance->0->>'class','unverified')
 FROM ranked ORDER BY score DESC,id LIMIT $6 OFFSET $7`, q.Query, q.SpaceIDs, kinds, q.Topics, vector, q.Limit, q.Offset, q.Principal.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []memory.Hit
	for rows.Next() {
		var h memory.Hit
		var kind string
		if err := rows.Scan(&h.MemoryID, &h.SpaceID, &h.Title, &kind, &h.Topics, &h.Excerpt, &h.Revision, &h.Score, &h.Provenance); err != nil {
			return nil, err
		}
		h.Kind = memory.Kind(kind)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (s *Store) Catalog(ctx context.Context, principalID string, readable []string, claims []memory.WorkspaceClaim) ([]memory.CatalogEntry, error) {
	claimKinds := make([]string, 0, len(claims))
	claimValues := make([]string, 0, len(claims))
	for _, claim := range claims {
		kind := strings.TrimSpace(claim.Kind)
		value := normalizeClaim(kind, claim.Value)
		if (kind == "git_remote" || kind == "local_path") && value != "" {
			claimKinds = append(claimKinds, kind)
			claimValues = append(claimValues, value)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT s.id::text,s.name,s.slug,s.type::text,
   coalesce((SELECT array_agg(topic ORDER BY count DESC,topic) FROM (
     SELECT topic,count(*) count FROM memories m JOIN memory_revisions r
       ON r.memory_id=m.id AND r.revision=m.current_revision,unnest(r.topics) topic
     WHERE m.space_id=s.id AND m.status='active' AND (r.valid_until IS NULL OR r.valid_until>now()) GROUP BY topic ORDER BY count DESC,topic LIMIT 6
   ) popular),'{}'::text[]) topics
	 FROM spaces s WHERE s.id=ANY($1::uuid[]) AND can_read_space($4,s.id) AND s.suspended_at IS NULL
 ORDER BY EXISTS(SELECT 1 FROM workspace_bindings wb
   WHERE wb.project_space_id=s.id AND (wb.claim_kind,wb.normalized_value) IN
	   (SELECT * FROM unnest($2::text[],$3::text[]))) DESC,s.name,s.id`, readable, claimKinds, claimValues, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memory.CatalogEntry
	for rows.Next() {
		var entry memory.CatalogEntry
		if err := rows.Scan(&entry.SpaceID, &entry.Name, &entry.Slug, &entry.Type, &entry.Topics); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func normalizeClaim(kind, value string) string {
	value = strings.TrimSpace(value)
	if kind == "git_remote" {
		value = strings.TrimSuffix(value, "/")
		value = strings.TrimSuffix(value, ".git")
		value = strings.TrimSuffix(value, "/")
		value = strings.ToLower(value)
	}
	if kind == "local_path" {
		value = strings.TrimSuffix(value, "/")
	}
	return value
}

func (s *Store) ResolveWorkspace(ctx context.Context, claims []memory.WorkspaceClaim) ([]string, error) {
	var kinds, values []string
	for _, claim := range claims {
		kind := strings.TrimSpace(claim.Kind)
		value := normalizeClaim(kind, claim.Value)
		if (kind == "git_remote" || kind == "local_path") && value != "" {
			kinds = append(kinds, kind)
			values = append(values, value)
		}
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT project_space_id::text FROM workspace_bindings
 WHERE (claim_kind,normalized_value) IN (SELECT * FROM unnest($1::text[],$2::text[])) ORDER BY 1`, kinds, values)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
