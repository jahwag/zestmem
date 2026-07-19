package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bytelope/zestmem/internal/memory"
	"github.com/bytelope/zestmem/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	v := make([]float32, 384)
	v[0] = 1
	return v, nil
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
	if err != nil || migrationCount != 3 {
		t.Fatalf("legacy schema migration count=%d, error=%v", migrationCount, err)
	}
	svc := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	cmd := memory.RememberCommand{Principal: memory.Principal{ID: agent}, SpaceID: project, Title: "Connection pool", Body: "Use a maximum of twenty database connections.", Kind: memory.KindDirective, Topics: []string{"postgres"}, Reason: "operational limit", IdempotencyKey: "create-1"}
	created, err := svc.Remember(ctx, cmd)
	if err != nil {
		t.Fatal(err)
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
	if len(hits) != 1 || hits[0].MemoryID != created.Memory.ID {
		t.Fatalf("hits=%#v", hits)
	}
	second, err := svc.Remember(ctx, memory.RememberCommand{
		Principal: cmd.Principal, SpaceID: project, Title: "Deployment channel", Body: "Use the stable release channel.",
		Kind: memory.KindFact, Topics: []string{"release"}, Reason: "document deployment", IdempotencyKey: "create-2", DuplicateResolution: "create_distinct",
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
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first browse page=%#v err=%v", firstPage, err)
	}
	secondPage, err := svc.BrowsePage(ctx, memory.BrowseQuery{Principal: cmd.Principal, SpaceIDs: []string{project}, Limit: 1, Cursor: firstPage.NextCursor})
	if err != nil || len(secondPage.Items) != 1 || secondPage.NextCursor != "" || secondPage.Items[0].MemoryID == firstPage.Items[0].MemoryID {
		t.Fatalf("second browse page=%#v err=%v", secondPage, err)
	}
	if firstPage.Items[0].MemoryID != second.Memory.ID && secondPage.Items[0].MemoryID != second.Memory.ID {
		t.Fatal("second memory missing from browse pages")
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
	revised, err := svc.Revise(ctx, memory.ReviseCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Connection pool", Body: "Use at most sixteen database connections.", Kind: memory.KindDirective, Topics: []string{"postgres"}, Reason: "capacity correction", IdempotencyKey: "revise-1"})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Memory.CurrentRevision.Number != 2 {
		t.Fatal("revision did not advance")
	}
	revisedRetry, err := svc.Revise(ctx, memory.ReviseCommand{Principal: cmd.Principal, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Connection pool", Body: "Use at most sixteen database connections.", Kind: memory.KindDirective, Topics: []string{"postgres"}, Reason: "capacity correction", IdempotencyKey: "revise-1"})
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
