package postgres_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/jahwag/zestmem/internal/postgres"
)

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	v := make([]float32, 384)
	v[0] = 1
	return v, nil
}

func TestUseWhenReveriePersistsAuthenticatedRetrievalStats(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`); err != nil {
		t.Fatal(err)
	}
	agent, owner, space := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES($1,'agent','Athena'),($2,'human','Owner')`, agent, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO spaces(id,type,name,slug) VALUES($1,'project','Project','project')`, space); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'contributor',$3)`, agent, space, owner); err != nil {
		t.Fatal(err)
	}

	svc := memory.NewService(store, store)
	created, err := svc.Remember(ctx, memory.RememberCommand{
		Principal:      memory.Principal{ID: agent},
		SpaceID:        space,
		Title:          "Unrelated deployment note",
		Body:           "The release train leaves on Tuesdays.",
		UseWhen:        "Investigating marmalade orbital drift.",
		Kind:           memory.KindFact,
		Reason:         "verify use_when retrieval",
		IdempotencyKey: "use-when-reverie",
	})
	if err != nil {
		t.Fatal(err)
	}
	reverie, err := svc.Reverie(ctx, memory.Principal{ID: agent}, "marmalade orbital drift")
	if err != nil {
		t.Fatal(err)
	}
	if len(reverie.Reveries) != 1 || reverie.Reveries[0].MemoryID != created.Memory.ID || !slices.Contains(reverie.Reveries[0].MatchReasons, "use_when") {
		t.Fatalf("use_when reverie=%#v", reverie)
	}
	if _, err := svc.Read(ctx, memory.Principal{ID: agent}, created.Memory.ID); err != nil {
		t.Fatal(err)
	}
	observed, err := store.ReadAuthorized(ctx, created.Memory.ID, memory.Principal{ID: agent})
	if err != nil {
		t.Fatal(err)
	}
	stats := observed.Retrieval
	if stats == nil || stats.RecallCount != 1 || stats.ReadCount != 1 || stats.LastRecalledBy != "Athena" || stats.LastReadBy != "Athena" || stats.LastRecalledAt == nil || stats.LastReadAt == nil {
		t.Fatalf("retrieval stats=%#v", stats)
	}
	var eventCount, targetCount int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM retrieval_events WHERE actor_id=$1`, agent).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM retrieval_event_memories WHERE memory_id=$1`, created.Memory.ID).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 || targetCount != 2 {
		t.Fatalf("events=%d targets=%d", eventCount, targetCount)
	}
}

func TestCueOnlyRevisionPreservesEmbeddingRecencyAndRejectsDisabledActor(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`); err != nil {
		t.Fatal(err)
	}
	agent, owner, space := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES($1,'agent','backfill'),($2,'human','owner')`, agent, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO spaces(id,type,name,slug) VALUES($1,'project','Project','project')`, space); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'contributor',$3)`, agent, space, owner); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	created, err := svc.Remember(ctx, memory.RememberCommand{
		Principal: memory.Principal{ID: agent}, SpaceID: space, Title: "Backfill target", Body: "Stable substantive body.",
		Kind: memory.KindFact, Reason: "create cue-only fixture", IdempotencyKey: "cue-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	current := created.Memory.CurrentRevision
	updated, err := svc.Revise(ctx, memory.ReviseCommand{
		Principal: memory.Principal{ID: agent}, MemoryID: created.Memory.ID, ExpectedRevision: 1,
		Title: current.Title, Body: current.Body, UseWhen: "Investigating cue-only revision behavior.", UseWhenSet: true,
		Kind: current.Kind, Topics: current.Topics, Provenance: current.Provenance,
		Reason: "reviewed cue backfill", IdempotencyKey: "cue-revise",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Memory.CurrentRevision.Number != 2 {
		t.Fatalf("revision=%d", updated.Memory.CurrentRevision.Number)
	}
	var contentUpdatedAt, sourceCreatedAt, cueCreatedAt time.Time
	var revisionCount, embeddingCount, auditCount int
	var embeddingAndFingerprintEqual bool
	err = conn.QueryRow(ctx, `SELECT m.content_updated_at,r1.created_at,r2.created_at,
 (SELECT count(*) FROM memory_revisions WHERE memory_id=m.id),
 (SELECT count(*) FROM memory_embeddings WHERE memory_id=m.id),
 (SELECT count(*) FROM audit_events WHERE actor_id=$2 AND action='revise' AND target_id=m.id::text),
 (SELECT (e1.embedding::text,e1.provider,e1.model,e1.model_revision,e1.dimensions,e1.normalization,e1.query_prefix,e1.document_prefix,e1.created_at)
         IS NOT DISTINCT FROM
         (e2.embedding::text,e2.provider,e2.model,e2.model_revision,e2.dimensions,e2.normalization,e2.query_prefix,e2.document_prefix,e2.created_at)
    FROM memory_embeddings e1 JOIN memory_embeddings e2 ON e2.memory_id=e1.memory_id
   WHERE e1.memory_id=m.id AND e1.revision=1 AND e2.revision=2)
 FROM memories m
 JOIN memory_revisions r1 ON r1.memory_id=m.id AND r1.revision=1
 JOIN memory_revisions r2 ON r2.memory_id=m.id AND r2.revision=2
 WHERE m.id=$1`, created.Memory.ID, agent).Scan(&contentUpdatedAt, &sourceCreatedAt, &cueCreatedAt, &revisionCount, &embeddingCount, &auditCount, &embeddingAndFingerprintEqual)
	if err != nil {
		t.Fatal(err)
	}
	if !contentUpdatedAt.Equal(sourceCreatedAt) || !cueCreatedAt.After(sourceCreatedAt) || revisionCount != 2 || embeddingCount != 2 || auditCount != 1 || !embeddingAndFingerprintEqual {
		t.Fatalf("content=%s source=%s cue=%s revisions=%d embeddings=%d audits=%d exact_copy=%v", contentUpdatedAt, sourceCreatedAt, cueCreatedAt, revisionCount, embeddingCount, auditCount, embeddingAndFingerprintEqual)
	}
	afterSubstantive := sourceCreatedAt.Add(time.Microsecond)
	browsed, err := svc.BrowsePage(ctx, memory.BrowseQuery{Principal: memory.Principal{ID: agent}, SpaceIDs: []string{space}, MemoryFilters: memory.MemoryFilters{RevisedFrom: &afterSubstantive}})
	if err != nil || browsed.TotalCount != 1 {
		t.Fatalf("browse after cue-only revision=%#v error=%v", browsed, err)
	}
	recalled, err := svc.RecallPage(ctx, memory.RecallQuery{Principal: memory.Principal{ID: agent}, SpaceIDs: []string{space}, Query: "stable substantive body", MemoryFilters: memory.MemoryFilters{RevisedFrom: &afterSubstantive}})
	if err != nil || recalled.TotalCount != 1 {
		t.Fatalf("recall after cue-only revision=%#v error=%v", recalled, err)
	}
	withoutVector := memory.NewService(store, store)
	missing, err := withoutVector.Remember(ctx, memory.RememberCommand{
		Principal: memory.Principal{ID: agent}, SpaceID: space, Title: "Missing vector", Body: "A legacy-style record without an embedding.",
		Kind: memory.KindFact, Reason: "create missing-vector fixture", IdempotencyKey: "missing-vector-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	missingCurrent := missing.Memory.CurrentRevision
	_, err = withoutVector.Revise(ctx, memory.ReviseCommand{
		Principal: memory.Principal{ID: agent}, MemoryID: missing.Memory.ID, ExpectedRevision: 1,
		Title: missingCurrent.Title, Body: missingCurrent.Body, UseWhen: "Investigating missing-vector behavior.", UseWhenSet: true,
		Kind: missingCurrent.Kind, Topics: missingCurrent.Topics, Provenance: missingCurrent.Provenance,
		Reason: "must roll back", IdempotencyKey: "missing-vector-revise",
	})
	if err == nil || !strings.Contains(err.Error(), "embedding is missing") {
		t.Fatalf("missing-vector revision error=%v", err)
	}
	var missingRevision, missingRevisionCount int
	if err := conn.QueryRow(ctx, `SELECT m.current_revision,(SELECT count(*) FROM memory_revisions r WHERE r.memory_id=m.id) FROM memories m WHERE m.id=$1`, missing.Memory.ID).Scan(&missingRevision, &missingRevisionCount); err != nil || missingRevision != 1 || missingRevisionCount != 1 {
		t.Fatalf("missing-vector rollback current=%d revisions=%d error=%v", missingRevision, missingRevisionCount, err)
	}
	if _, err := conn.Exec(ctx, `UPDATE principals SET disabled_at=now() WHERE id=$1`, agent); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Revise(ctx, memory.ReviseCommand{
		Principal: memory.Principal{ID: agent}, MemoryID: created.Memory.ID, ExpectedRevision: 2,
		Title: current.Title, Body: current.Body, UseWhen: "Another cue.", UseWhenSet: true,
		Kind: current.Kind, Topics: current.Topics, Provenance: current.Provenance,
		Reason: "must be denied", IdempotencyKey: "disabled-revise",
	})
	if !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("disabled actor error=%v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM memory_revisions WHERE memory_id=$1`, created.Memory.ID).Scan(&revisionCount); err != nil || revisionCount != 2 {
		t.Fatalf("revision count after disabled write=%d error=%v", revisionCount, err)
	}
}

func TestPostgresMemoryLifecycleAndIsolation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	agent := uuid.NewString()
	other := uuid.NewString()
	owner := uuid.NewString()
	org := uuid.NewString()
	project := uuid.NewString()
	_, err = conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES($1,'agent','agent'),($2,'agent','other'),($3,'human','owner')`, agent, other, owner)
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(id,type,name,slug) VALUES($1,'organization','Org','org')`, org)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(id,type,parent_id,name,slug) VALUES($1,'project',$2,'Project','project')`, project, org)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'contributor',$3),($4,$5,'reader',$3)`, agent, project, owner, other, org)
	}
	if err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	verifyMigrations, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	err = verifyMigrations.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount)
	verifyMigrations.Close(ctx)
	if err != nil || migrationCount != 10 {
		t.Fatalf("legacy schema migration count=%d, error=%v", migrationCount, err)
	}
	svc := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	cmd := memory.RememberCommand{Principal: memory.Principal{ID: agent}, SpaceID: project, Title: "Connection pool", Body: "Use a maximum of twenty database connections.", UseWhen: "Reviewing or debugging database connection limits.", Kind: memory.KindDirective, Topics: []string{"postgres"}, Provenance: []memory.Provenance{{Class: "user_instruction", Locator: "conversation", Attribution: "User One"}}, Reason: "operational limit", IdempotencyKey: "create-1", ConfirmNewTopics: []string{"postgres"}}
	created, err := svc.Remember(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if created.Memory.CurrentRevision.ActorDisplayName != "agent" || created.Memory.CurrentRevision.Provenance[0].Attribution != "User One" {
		t.Fatalf("attribution=%#v actor=%q", created.Memory.CurrentRevision.Provenance, created.Memory.CurrentRevision.ActorDisplayName)
	}
	assertionConn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var assertionID string
	err = assertionConn.QueryRow(ctx, `INSERT INTO memory_principal_assertions
	 (memory_id,relationship,principal_id,basis_revision,basis_class,basis_locator,asserted_by,reason)
	 VALUES($1,'intended_agent',$2,1,'legacy_role_path','worker/athena',$3,'Legacy worker collection mapping')
	 RETURNING id::text`, created.Memory.ID, other, owner).Scan(&assertionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assertionConn.Exec(ctx, `UPDATE memory_principal_assertions SET reason='rewritten' WHERE id=$1`, assertionID); err == nil {
		t.Fatal("append-only assertion accepted an update")
	}
	if _, err = assertionConn.Exec(ctx, `INSERT INTO memory_principal_assertions
	 (memory_id,relationship,principal_id,basis_revision,basis_class,asserted_by,reason)
	 VALUES($1,'intended_agent',$2,1,'legacy_role_path',$3,'conflict')`, created.Memory.ID, agent, owner); err == nil {
		t.Fatal("second active intended-agent assertion was accepted")
	}
	if _, err = assertionConn.Exec(ctx, `INSERT INTO memory_principal_assertion_retractions(assertion_id,retracted_by,retracted_at,reason)
	 VALUES($1,$2,'2000-01-01T00:00:00Z','invalid chronology')`, assertionID, owner); err == nil {
		t.Fatal("retraction before assertion was accepted")
	}
	if _, err = assertionConn.Exec(ctx, `INSERT INTO memory_principal_assertion_retractions(assertion_id,retracted_by,reason)
	 VALUES($1,$2,'Correct the administrative association')`, assertionID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = assertionConn.Exec(ctx, `INSERT INTO memory_principal_assertions
	 (memory_id,relationship,principal_id,basis_revision,basis_class,basis_locator,asserted_by,reason)
	 VALUES($1,'intended_agent',$2,1,'legacy_role_path','worker/athena',$3,'Reviewed legacy worker mapping')`,
		created.Memory.ID, other, owner); err != nil {
		t.Fatal(err)
	}
	var assertionAudits int
	if err = assertionConn.QueryRow(ctx, `SELECT count(*) FROM audit_events
	 WHERE target_type='memory_principal_assertion' AND action IN ('assert_memory_principal','retract_memory_principal_assertion')`).Scan(&assertionAudits); err != nil || assertionAudits != 3 {
		t.Fatalf("assertion audit count=%d err=%v", assertionAudits, err)
	}
	assertionConn.Close(ctx)
	associated, err := svc.Read(ctx, cmd.Principal, created.Memory.ID)
	if err != nil || len(associated.HistoricalAssociations) != 1 {
		t.Fatalf("historical associations=%#v err=%v", associated.HistoricalAssociations, err)
	}
	association := associated.HistoricalAssociations[0]
	if association.Relationship != "intended_agent" || association.PrincipalID != other || association.PrincipalDisplayName != "other" || association.AssertedBy != owner || associated.CurrentRevision.ActorID != agent {
		t.Fatalf("historical association=%#v", association)
	}
	segment, err := svc.ReadSegment(ctx, memory.ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID})
	if err != nil || len(segment.HistoricalAssociations) != 1 || segment.HistoricalAssociations[0].BasisRevision != 1 || segment.HistoricalAssociations[0].AssertedBy != owner || segment.HistoricalAssociations[0].Reason == "" {
		t.Fatalf("segment historical associations=%#v err=%v", segment.HistoricalAssociations, err)
	}

	constraintConn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	unsafeUseWhen := map[string]string{
		"too long":       strings.Repeat("x", 241),
		"control":        "unsafe\u001fvalue",
		"soft hyphen":    "unsafe\u00advalue",
		"zero width":     "unsafe\u200bvalue",
		"line separator": "unsafe\u2028value",
		"bidi override":  "unsafe\u202evalue",
		"noncharacter":   "unsafe\ufdd0value",
	}
	for name, value := range unsafeUseWhen {
		_, constraintErr := constraintConn.Exec(ctx, `UPDATE memory_revisions SET use_when=$2 WHERE memory_id=$1`, created.Memory.ID, value)
		var pgErr *pgconn.PgError
		if !errors.As(constraintErr, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "memory_revisions_use_when_check" {
			constraintConn.Close(ctx)
			t.Fatalf("database accepted unsafe use_when %s: %v", name, constraintErr)
		}
	}
	constraintConn.Close(ctx)
	plainService := memory.NewService(store, store)
	if cueOnlyHits, err := plainService.Recall(ctx, memory.RecallQuery{Principal: cmd.Principal, Query: "debugging"}); err != nil {
		t.Fatal(err)
	} else if len(cueOnlyHits) != 1 || cueOnlyHits[0].MemoryID != created.Memory.ID || !slices.Contains(cueOnlyHits[0].MatchReasons, "use_when") {
		t.Fatalf("use_when was not an indexed match signal: %#v", cueOnlyHits)
	}
	if err := store.VerifyEmbeddingFingerprint(ctx); err != nil {
		t.Fatalf("valid active embedding fingerprint rejected: %v", err)
	}
	retried, err := svc.Remember(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Memory.ID != created.Memory.ID {
		t.Fatalf("idempotent IDs differ")
	}
	dup := cmd
	dup.IdempotencyKey = "create-duplicate"
	_, err = svc.Remember(ctx, dup)
	if !errors.Is(err, memory.ErrDuplicate) {
		t.Fatalf("duplicate error=%v", err)
	}
	hits, err := svc.Recall(ctx, memory.RecallQuery{Principal: cmd.Principal, Query: "database connection maximum"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MemoryID != created.Memory.ID || hits[0].UseWhen != cmd.UseWhen {
		t.Fatalf("hits=%#v", hits)
	}
	validUntil := time.Now().UTC().Add(48 * time.Hour)
	second, err := svc.Remember(ctx, memory.RememberCommand{
		Principal: cmd.Principal, SpaceID: project, Title: "Deployment channel", Body: "Use the stable release channel.",
		Kind: memory.KindFact, Topics: []string{"release"}, ValidUntil: &validUntil, Reason: "document deployment", IdempotencyKey: "create-2", DuplicateResolution: "create_distinct", ConfirmNewTopics: []string{"release"},
	})
	if err != nil {
		t.Fatal(err)
	}
	browsed, err := svc.BrowsePage(ctx, memory.BrowseQuery{
		Principal: cmd.Principal, SpaceIDs: []string{project}, Kinds: []memory.Kind{memory.KindDirective},
		Topics: []string{"postgres"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(browsed.Items) != 1 || browsed.Items[0].MemoryID != created.Memory.ID || browsed.Items[0].UpdatedAt.IsZero() {
		t.Fatalf("browse=%#v", browsed)
	}
	firstPage, err := svc.BrowsePage(ctx, memory.BrowseQuery{Principal: cmd.Principal, SpaceIDs: []string{project}, Limit: 1})
	if err != nil || len(firstPage.Items) != 1 || firstPage.TotalCount != 2 || firstPage.NextCursor == "" {
		t.Fatalf("first browse page=%#v err=%v", firstPage, err)
	}
	secondPage, err := svc.BrowsePage(ctx, memory.BrowseQuery{Principal: cmd.Principal, SpaceIDs: []string{project}, Limit: 1, Cursor: firstPage.NextCursor})
	if err != nil || len(secondPage.Items) != 1 || secondPage.TotalCount != 2 || secondPage.NextCursor != "" || secondPage.Items[0].MemoryID == firstPage.Items[0].MemoryID {
		t.Fatalf("second browse page=%#v err=%v", secondPage, err)
	}
	if firstPage.Items[0].MemoryID != second.Memory.ID && secondPage.Items[0].MemoryID != second.Memory.ID {
		t.Fatal("second memory missing from browse pages")
	}
	topics, err := svc.DiscoverMetadata(ctx, memory.MetadataQuery{Facet: memory.MetadataTopic, Principal: cmd.Principal, SpaceIDs: []string{project}, Limit: 1})
	if err != nil || topics.TotalCount != 2 || len(topics.Values) != 1 || topics.NextCursor == "" {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	attributions, err := svc.DiscoverMetadata(ctx, memory.MetadataQuery{Facet: memory.MetadataProvenanceAttribution, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || attributions.TotalCount != 1 || attributions.Values[0].Value != "User One" {
		t.Fatalf("attributions=%#v err=%v", attributions, err)
	}
	filtered, err := svc.BrowsePage(ctx, memory.BrowseQuery{MemoryFilters: memory.MemoryFilters{Revision: func() *int { value := 1; return &value }(), ProvenanceAttributions: []string{"User One"}, RememberedFrom: &created.Memory.CreatedAt}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || filtered.TotalCount != 1 || filtered.Items[0].MemoryID != created.Memory.ID {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
	revisedFrom := second.Memory.CurrentRevision.CreatedAt.Add(-time.Second)
	revisedTo := second.Memory.CurrentRevision.CreatedAt.Add(time.Second)
	validFrom := validUntil.Add(-time.Second)
	validTo := validUntil.Add(time.Second)
	spans, err := svc.BrowsePage(ctx, memory.BrowseQuery{MemoryFilters: memory.MemoryFilters{RevisedFrom: &revisedFrom, RevisedTo: &revisedTo, ValidFrom: &validFrom, ValidTo: &validTo}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || spans.TotalCount != 1 || spans.Items[0].MemoryID != second.Memory.ID {
		t.Fatalf("date spans=%#v err=%v", spans, err)
	}
	mixed, err := svc.Remember(ctx, memory.RememberCommand{
		Principal: cmd.Principal, SpaceID: project, Title: "Mixed source provenance", Body: "A source-pairing filter regression fixture.",
		Kind: memory.KindFact, Topics: []string{"release"}, Reason: "test provenance pairs", IdempotencyKey: "create-3", DuplicateResolution: "create_distinct",
		Provenance: []memory.Provenance{{Class: "User_Instruction", Locator: "conversation", Attribution: "Bob"}, {Class: "web", Locator: "https://example.test", Attribution: "Alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyClass, err := svc.DiscoverMetadata(ctx, memory.MetadataQuery{Facet: memory.MetadataProvenanceClass, Query: "user_", Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || legacyClass.TotalCount != 1 || legacyClass.Values[0].Value != "user_instruction" {
		t.Fatalf("normalized provenance discovery=%#v err=%v", legacyClass, err)
	}
	wildcard, err := svc.DiscoverMetadata(ctx, memory.MetadataQuery{Facet: memory.MetadataProvenanceClass, Query: "%", Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || wildcard.TotalCount != 0 {
		t.Fatalf("wildcard metadata query=%#v err=%v", wildcard, err)
	}
	paired, err := svc.BrowsePage(ctx, memory.BrowseQuery{MemoryFilters: memory.MemoryFilters{ProvenanceClasses: []string{"user_instruction"}, ProvenanceAttributions: []string{"Alice"}}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || paired.TotalCount != 0 {
		t.Fatalf("cross-entry provenance false positive=%#v err=%v", paired, err)
	}
	paired, err = svc.BrowsePage(ctx, memory.BrowseQuery{MemoryFilters: memory.MemoryFilters{ProvenanceClasses: []string{" USER_Instruction "}, ProvenanceLocators: []string{"conversation"}, ProvenanceAttributions: []string{"Bob"}}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || paired.TotalCount != 1 || paired.Items[0].MemoryID != mixed.Memory.ID {
		t.Fatalf("same-entry normalized provenance=%#v err=%v", paired, err)
	}
	filteredRecall, err := svc.RecallPage(ctx, memory.RecallQuery{MemoryFilters: memory.MemoryFilters{ProvenanceClasses: []string{"user_instruction"}, ProvenanceAttributions: []string{"Alice"}}, Principal: cmd.Principal, SpaceIDs: []string{project}, Query: "source pairing regression"})
	if err != nil || filteredRecall.TotalCount != 0 {
		t.Fatalf("filtered recall cross-entry false positive=%#v err=%v", filteredRecall, err)
	}
	_, err = svc.Read(ctx, memory.Principal{ID: other}, created.Memory.ID)
	if err != nil {
		t.Fatalf("inherited descendant read failed: %v", err)
	}
	unauthorized := memory.Principal{ID: uuid.NewString()}
	_, err = svc.Read(ctx, unauthorized, created.Memory.ID)
	if !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("unauthorized read error=%v", err)
	}
	adminSuspend, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adminSuspend.Exec(ctx, `UPDATE spaces SET suspended_at=now() WHERE id=$1`, project)
	adminSuspend.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Read(ctx, cmd.Principal, created.Memory.ID)
	if !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("suspended Space read error=%v", err)
	}
	hits, err = svc.Recall(ctx, memory.RecallQuery{Principal: cmd.Principal, Query: "database connection maximum"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatal("suspended Space appeared in recall")
	}
	adminSuspend, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adminSuspend.Exec(ctx, `UPDATE spaces SET suspended_at=NULL WHERE id=$1`, project)
	adminSuspend.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := svc.Revise(ctx, memory.ReviseCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Connection pool", Body: "Use at most sixteen database connections.", UseWhen: "Changing or troubleshooting database pool limits.", UseWhenSet: true, Kind: memory.KindDirective, Topics: []string{"postgres"}, Provenance: cmd.Provenance, Reason: "capacity correction", IdempotencyKey: "revise-1"})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Memory.CurrentRevision.Number != 2 || revised.Memory.CurrentRevision.UseWhen != "Changing or troubleshooting database pool limits." {
		t.Fatal("revision did not advance")
	}
	revisedRetry, err := svc.Revise(ctx, memory.ReviseCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Connection pool", Body: "Use at most sixteen database connections.", UseWhen: "Changing or troubleshooting database pool limits.", UseWhenSet: true, Kind: memory.KindDirective, Topics: []string{"postgres"}, Provenance: cmd.Provenance, Reason: "capacity correction", IdempotencyKey: "revise-1"})
	if err != nil {
		t.Fatal(err)
	}
	if revisedRetry.Memory.CurrentRevision.Number != 2 {
		t.Fatal("revision retry was not idempotent")
	}
	_, err = svc.Retract(ctx, memory.RetractCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 2, Reason: "limit removed", IdempotencyKey: "retract-1"})
	if !errors.Is(err, memory.ErrForbidden) {
		t.Fatalf("contributor retract error=%v", err)
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `UPDATE space_grants SET role='maintainer' WHERE principal_id=$1 AND space_id=$2`, agent, project)
	admin.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Retract(ctx, memory.RetractCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 2, Reason: "limit removed", IdempotencyKey: "retract-2"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Retract(ctx, memory.RetractCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 2, Reason: "limit removed", IdempotencyKey: "retract-2"})
	if err != nil {
		t.Fatalf("retract retry: %v", err)
	}
	_, err = svc.Read(ctx, memory.Principal{ID: other}, created.Memory.ID)
	if !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("reader inspected retracted body: %v", err)
	}
	if _, err = svc.Read(ctx, cmd.Principal, created.Memory.ID); err != nil {
		t.Fatalf("maintainer could not inspect retracted memory: %v", err)
	}
	retracted, err := svc.BrowsePage(ctx, memory.BrowseQuery{MemoryFilters: memory.MemoryFilters{Statuses: []memory.Status{memory.StatusRetracted}}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || retracted.TotalCount != 1 || retracted.Items[0].MemoryID != created.Memory.ID {
		t.Fatalf("retracted browse=%#v err=%v", retracted, err)
	}
	retractedAttribution, err := svc.DiscoverMetadata(ctx, memory.MetadataQuery{Facet: memory.MetadataProvenanceAttribution, Query: "User One", Statuses: []memory.Status{memory.StatusRetracted}, Principal: cmd.Principal, SpaceIDs: []string{project}})
	if err != nil || retractedAttribution.TotalCount != 1 {
		t.Fatalf("retracted provenance discovery=%#v err=%v", retractedAttribution, err)
	}
	hits, err = svc.Recall(ctx, memory.RecallQuery{Principal: cmd.Principal, Query: "database connection maximum"})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.MemoryID == created.Memory.ID {
			t.Fatalf("retracted memory returned: %#v", hits)
		}
	}
}
