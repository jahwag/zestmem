package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCatalogAndRecallStayInsideContextEnvelopes(t *testing.T) {
	svc, repo, _ := testService()
	repo.catalog = make([]CatalogEntry, 100)
	for i := range repo.catalog {
		repo.catalog[i] = CatalogEntry{SpaceID: strings.Repeat("i", 36), Name: strings.Repeat("n", 100), Slug: strings.Repeat("s", 80), Type: "project", Topics: []string{strings.Repeat("t", 80)}}
	}
	catalog, err := svc.Catalog(context.Background(), Principal{ID: "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(catalog)
	if tokens := conservativeTokenCount(raw); tokens > 800 {
		t.Fatalf("catalog tokens=%d", tokens)
	}
	if catalog.OmittedCount == 0 {
		t.Fatal("catalog did not report omissions")
	}
	if catalog.NextCursor == "" {
		t.Fatal("catalog did not return pagination cursor")
	}
	next, err := svc.Catalog(context.Background(), Principal{ID: "agent"}, nil, catalog.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Spaces) == 0 {
		t.Fatal("catalog second page is empty")
	}
	repo.hits = make([]Hit, 20)
	for i := range repo.hits {
		repo.hits[i] = Hit{MemoryID: strings.Repeat("m", 36), SpaceID: "project", Title: strings.Repeat("x", 300), Excerpt: strings.Repeat("body-marker", 100), Topics: []string{"topic"}}
	}
	hits, err := svc.Recall(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "specific task", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(hits)
	if tokens := conservativeTokenCount(raw); tokens > 2000 {
		t.Fatalf("recall tokens=%d", tokens)
	}
	if len(hits) >= 20 {
		t.Fatal("recall budget did not trim oversized hits")
	}
}

func TestRecallCursorPagesStableRankedHits(t *testing.T) {
	svc, repo, _ := testService()
	repo.hits = make([]Hit, 7)
	for i := range repo.hits {
		repo.hits[i] = Hit{MemoryID: fmt.Sprintf("m-%d", i), SpaceID: "project", Title: "hit", Excerpt: "small"}
	}
	first, err := svc.RecallPage(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "task", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hits) != 3 || first.NextCursor == "" {
		t.Fatalf("first=%#v", first)
	}
	second, err := svc.RecallPage(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "task", Limit: 3, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Hits) != 3 || second.Hits[0].MemoryID == first.Hits[0].MemoryID {
		t.Fatalf("second=%#v", second)
	}
}

func TestBrowseCursorPagesAndFiltersAreAuthorizationBound(t *testing.T) {
	svc, repo, auth := testService()
	auth.spaces = []string{"project", "other-readable"}
	repo.browse = []BrowseItem{
		{MemoryID: "10000000-0000-0000-0000-000000000001", SpaceID: "project", Title: "Newest", Kind: KindDecision, UpdatedAt: time.Unix(300, 0)},
		{MemoryID: "10000000-0000-0000-0000-000000000002", SpaceID: "project", Title: "Next", Kind: KindDecision, UpdatedAt: time.Unix(200, 0)},
		{MemoryID: "10000000-0000-0000-0000-000000000003", SpaceID: "project", Title: "Oldest", Kind: KindDecision, UpdatedAt: time.Unix(100, 0)},
	}
	first, err := svc.BrowsePage(context.Background(), BrowseQuery{
		Principal: Principal{ID: "agent"}, SpaceIDs: []string{"project", "not-readable"},
		Kinds: []Kind{KindDecision}, Topics: []string{" Architecture "}, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" || repo.browseQuery.SpaceIDs[0] != "project" || len(repo.browseQuery.SpaceIDs) != 1 {
		t.Fatalf("first=%#v query=%#v", first, repo.browseQuery)
	}
	if len(repo.browseQuery.Topics) != 1 || repo.browseQuery.Topics[0] != "architecture" {
		t.Fatalf("topics=%#v", repo.browseQuery.Topics)
	}
	second, err := svc.BrowsePage(context.Background(), BrowseQuery{
		Principal: Principal{ID: "agent"}, SpaceIDs: []string{"project", "not-readable"},
		Kinds: []Kind{KindDecision}, Topics: []string{"architecture"}, Limit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].MemoryID != "10000000-0000-0000-0000-000000000003" || second.NextCursor != "" {
		t.Fatalf("second=%#v", second)
	}
	_, err = svc.BrowsePage(context.Background(), BrowseQuery{
		Principal: Principal{ID: "agent"}, SpaceIDs: []string{"project"}, Kinds: []Kind{KindFact}, Limit: 2, Cursor: first.NextCursor,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor reused with changed filters: %v", err)
	}
}

func TestBrowseFailsClosedWithoutReadableSpaces(t *testing.T) {
	svc, repo, auth := testService()
	auth.spaces = nil
	result, err := svc.BrowsePage(context.Background(), BrowseQuery{Principal: Principal{ID: "agent"}})
	if err != nil || len(result.Items) != 0 || repo.browseCalls != 0 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, repo.browseCalls)
	}
	auth.spaces = []string{"readable"}
	result, err = svc.BrowsePage(context.Background(), BrowseQuery{Principal: Principal{ID: "agent"}, SpaceIDs: []string{"forbidden"}})
	if err != nil || len(result.Items) != 0 || repo.browseCalls != 0 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, repo.browseCalls)
	}
}

func TestEscapingCannotCreateAnEmptyOrOverBudgetPage(t *testing.T) {
	svc, repo, _ := testService()
	repo.hits = []Hit{{MemoryID: "m", SpaceID: "project", Title: strings.Repeat("\"", 512), Excerpt: strings.Repeat("\\", 400), Topics: []string{strings.Repeat("!", 2000)}}}
	page, err := svc.RecallPage(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 {
		t.Fatalf("page=%#v", page)
	}
	raw, _ := json.Marshal(page)
	if tokens := conservativeTokenCount(raw); tokens > 2000 {
		t.Fatalf("tokens=%d", tokens)
	}
}

func TestRememberRequiresExplicitMutationMetadata(t *testing.T) {
	svc, _, _ := testService()
	_, err := svc.Remember(context.Background(), RememberCommand{
		Principal: Principal{ID: "agent"}, SpaceID: "project",
		Title: "A fact", Body: "Useful body", Kind: KindFact,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Remember error = %v, want ErrInvalidInput", err)
	}
}

func TestRememberRejectsOversizedBody(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.Body = strings.Repeat("x", MaxBodyBytes+1)
	_, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Remember error = %v, want ErrInvalidInput", err)
	}
}

func TestRememberNormalizesTopicsAndDefaultsProvenance(t *testing.T) {
	svc, repo, _ := testService()
	result, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written {
		t.Fatal("Remember did not write")
	}
	got := repo.memories[result.Memory.ID].CurrentRevision
	if len(got.Topics) != 2 || got.Topics[0] != "api design" || got.Topics[1] != "go" {
		t.Fatalf("topics = %#v", got.Topics)
	}
	if len(got.Provenance) != 1 || got.Provenance[0].Class != "unverified" {
		t.Fatalf("provenance = %#v", got.Provenance)
	}
}

func TestRememberReturnsNearDuplicateWithoutWriting(t *testing.T) {
	svc, repo, _ := testService()
	repo.similar = []DuplicateCandidate{{MemoryID: "old", Title: "Existing", Score: .91}}
	result, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	if result.Written || len(result.DuplicateCandidates) != 1 || len(repo.memories) != 0 {
		t.Fatalf("result = %#v, memories = %d", result, len(repo.memories))
	}
}

func TestRememberCreateDistinctOverridesNearDuplicateAdvisory(t *testing.T) {
	svc, repo, _ := testService()
	repo.similar = []DuplicateCandidate{{MemoryID: "old", Title: "Existing", Score: .91}}
	cmd := validRemember()
	cmd.DuplicateResolution = "create_distinct"
	result, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || len(repo.memories) != 1 {
		t.Fatalf("result = %#v, memories = %d", result, len(repo.memories))
	}
}

func TestRememberIsIdempotent(t *testing.T) {
	svc, repo, _ := testService()
	cmd := validRemember()
	first, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Memory.ID != second.Memory.ID || len(repo.memories) != 1 {
		t.Fatalf("first=%s second=%s count=%d", first.Memory.ID, second.Memory.ID, len(repo.memories))
	}
	if !first.Written || first.Replayed || second.Written || !second.Replayed {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestIdempotencyKeyCannotBeReusedForDifferentInput(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	if _, err := svc.Remember(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	cmd.Body = "Different durable fact"
	_, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("error=%v", err)
	}
}

func TestReviseUsesOptimisticConcurrency(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Revise(context.Background(), ReviseCommand{
		Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID,
		ExpectedRevision: 2, Title: "Updated", Body: "Updated body",
		Kind: KindFact, Reason: "Correct wording", IdempotencyKey: "rev-1",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Revise error = %v, want ErrConflict", err)
	}
}

func TestReviseCreatesImmutableNextRevision(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Revise(context.Background(), ReviseCommand{
		Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID,
		ExpectedRevision: 1, Title: "Updated", Body: "Updated body",
		Kind: KindFact, Reason: "Correct wording", IdempotencyKey: "rev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Memory.CurrentRevision.Number != 2 || result.Memory.CurrentRevision.Body != "Updated body" {
		t.Fatalf("revision = %#v", result.Memory.CurrentRevision)
	}
}

func TestReviseRetryReturnsOriginalResultAfterRevisionAdvanced(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	cmd := ReviseCommand{Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Updated", Body: "Updated body", Kind: KindFact, Reason: "Correct wording", IdempotencyKey: "rev-retry"}
	first, err := svc.Revise(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Revise(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Memory.CurrentRevision.Number != 2 || second.Memory.CurrentRevision.Number != 2 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if !first.Written || first.Replayed || second.Written || !second.Replayed {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestRetractRemovesMemoryFromRecall(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Retract(context.Background(), RetractCommand{
		Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID,
		ExpectedRevision: 1, Reason: "No longer true", IdempotencyKey: "ret-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := svc.Recall(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("Recall returned retracted memory: %#v", hits)
	}
}

func TestRetractIsIdempotent(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	cmd := RetractCommand{Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, ExpectedRevision: 1, Reason: "No longer true", IdempotencyKey: "ret-retry"}
	first, err := svc.Retract(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Retract(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.Memory.Status != StatusRetracted || second.Memory.Status != StatusRetracted {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if !first.Written || first.Replayed || second.Written || !second.Replayed {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestReadHidesUnauthorizedMemory(t *testing.T) {
	svc, _, auth := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	auth.err = ErrForbidden
	_, err = svc.Read(context.Background(), Principal{ID: "other"}, created.Memory.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read error = %v, want ErrNotFound", err)
	}
}

func TestVisibleReaderGetsForbiddenForRevision(t *testing.T) {
	svc, _, auth := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	auth.actionErr = map[Action]error{ActionRevise: ErrForbidden}
	_, err = svc.Revise(context.Background(), ReviseCommand{Principal: Principal{ID: "reader"}, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Updated", Body: "Updated body", Kind: KindFact, Reason: "attempt", IdempotencyKey: "reader-revise"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
}

func validRemember() RememberCommand {
	return RememberCommand{
		Principal: Principal{ID: "agent"}, SpaceID: "project",
		Title: "A fact", Body: "Useful body", Kind: KindFact,
		Topics: []string{" API   Design ", "Go", "go"},
		Reason: "Operator requested durable knowledge", IdempotencyKey: "create-1",
	}
}

func testService() (*Service, *fakeRepo, *fakeAuth) {
	auth := &fakeAuth{spaces: []string{"project"}}
	repo := &fakeRepo{memories: map[string]Memory{}, idempotency: map[string]MutationResult{}, requestHashes: map[string]string{}, auth: auth}
	svc := NewService(repo, auth)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	svc.id = func() string { return "memory-1" }
	return svc, repo, auth
}

type fakeAuth struct {
	spaces    []string
	err       error
	actionErr map[Action]error
}

func (a *fakeAuth) Require(_ context.Context, _ Principal, _ string, action Action) error {
	if err := a.actionErr[action]; err != nil {
		return err
	}
	return a.err
}
func (a *fakeAuth) ReadableSpaces(context.Context, Principal) ([]string, error) {
	return a.spaces, a.err
}

type fakeRepo struct {
	memories      map[string]Memory
	idempotency   map[string]MutationResult
	requestHashes map[string]string
	similar       []DuplicateCandidate
	catalog       []CatalogEntry
	hits          []Hit
	browse        []BrowseItem
	browseQuery   BrowseQuery
	browseCalls   int
	auth          Authorizer
}

func (r *fakeRepo) idemKey(principal, op, key string) string { return principal + "/" + op + "/" + key }

func (r *fakeRepo) FindIdempotent(_ context.Context, principal, op, key, requestHash string) (MutationResult, bool, error) {
	idemKey := r.idemKey(principal, op, key)
	v, ok := r.idempotency[idemKey]
	if ok && r.requestHashes[idemKey] != requestHash {
		return MutationResult{}, false, ErrIdempotencyReuse
	}
	return v, ok, nil
}

func (r *fakeRepo) FindExact(_ context.Context, _, space, hash, exclude string) (*Memory, error) {
	for _, mem := range r.memories {
		if mem.SpaceID == space && mem.ID != exclude && mem.CurrentRevision.ContentHash == hash && mem.Status == StatusActive {
			copy := mem
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *fakeRepo) FindSimilar(context.Context, string, string, string, []float32, int) ([]DuplicateCandidate, error) {
	return r.similar, nil
}

func (r *fakeRepo) Create(_ context.Context, rec CreateRecord) (Memory, error) {
	rev := Revision{MemoryID: rec.NewID, Number: 1, Title: rec.Title, Body: rec.Body, Kind: rec.Kind,
		Topics: rec.Topics, Provenance: rec.Provenance, ValidUntil: rec.ValidUntil,
		ContentHash: rec.ContentHash, ActorID: rec.PrincipalID, CreatedAt: rec.Now, Reason: rec.Reason}
	mem := Memory{ID: rec.NewID, SpaceID: rec.SpaceID, Status: StatusActive, CurrentRevision: rev, CreatedAt: rec.Now}
	r.memories[mem.ID] = mem
	result := MutationResult{Memory: mem, Written: true}
	r.idempotency[r.idemKey(rec.PrincipalID, "remember", rec.IdempotencyKey)] = result
	r.requestHashes[r.idemKey(rec.PrincipalID, "remember", rec.IdempotencyKey)] = rec.RequestHash
	return mem, nil
}

func (r *fakeRepo) ReadAuthorized(ctx context.Context, id string, principal Principal) (Memory, error) {
	mem, ok := r.memories[id]
	if !ok {
		return Memory{}, ErrNotFound
	}
	action := ActionRead
	if mem.Status == StatusRetracted {
		action = ActionRetract
	}
	if err := r.auth.Require(ctx, principal, mem.SpaceID, action); err != nil {
		return Memory{}, ErrNotFound
	}
	return mem, nil
}

func (r *fakeRepo) Revise(_ context.Context, rec ReviseRecord) (Memory, error) {
	mem := r.memories[rec.MemoryID]
	if mem.CurrentRevision.Number != rec.ExpectedRevision {
		return Memory{}, ErrConflict
	}
	mem.CurrentRevision = Revision{MemoryID: rec.MemoryID, Number: rec.ExpectedRevision + 1,
		Title: rec.Title, Body: rec.Body, Kind: rec.Kind, Topics: rec.Topics,
		Provenance: rec.Provenance, ValidUntil: rec.ValidUntil, ContentHash: rec.ContentHash,
		ActorID: rec.PrincipalID, CreatedAt: rec.Now, Reason: rec.Reason}
	r.memories[mem.ID] = mem
	result := MutationResult{Memory: mem, Written: true}
	r.idempotency[r.idemKey(rec.PrincipalID, "revise", rec.IdempotencyKey)] = result
	r.requestHashes[r.idemKey(rec.PrincipalID, "revise", rec.IdempotencyKey)] = rec.RequestHash
	return mem, nil
}

func (r *fakeRepo) Retract(_ context.Context, rec RetractRecord) (Memory, error) {
	mem := r.memories[rec.MemoryID]
	if mem.CurrentRevision.Number != rec.ExpectedRevision {
		return Memory{}, ErrConflict
	}
	mem.Status = StatusRetracted
	r.memories[mem.ID] = mem
	result := MutationResult{Memory: mem, Written: true}
	r.idempotency[r.idemKey(rec.PrincipalID, "retract", rec.IdempotencyKey)] = result
	r.requestHashes[r.idemKey(rec.PrincipalID, "retract", rec.IdempotencyKey)] = rec.RequestHash
	return mem, nil
}

func (r *fakeRepo) Browse(_ context.Context, q BrowseQuery) ([]BrowseItem, error) {
	r.browseCalls++
	r.browseQuery = q
	if r.browse != nil {
		start := 0
		if q.BeforeMemoryID != "" {
			for i := range r.browse {
				if r.browse[i].MemoryID == q.BeforeMemoryID {
					start = i + 1
					break
				}
			}
		}
		if start >= len(r.browse) {
			return nil, nil
		}
		end := min(len(r.browse), start+q.Limit)
		return r.browse[start:end], nil
	}
	var items []BrowseItem
	for _, mem := range r.memories {
		if mem.Status != StatusActive || (len(q.SpaceIDs) > 0 && !contains(q.SpaceIDs, mem.SpaceID)) {
			continue
		}
		if len(q.Kinds) > 0 && !containsKind(q.Kinds, mem.CurrentRevision.Kind) {
			continue
		}
		items = append(items, BrowseItem{
			MemoryID: mem.ID, SpaceID: mem.SpaceID, Title: mem.CurrentRevision.Title,
			Kind: mem.CurrentRevision.Kind, Topics: mem.CurrentRevision.Topics,
			Excerpt: excerpt(mem.CurrentRevision.Body, 240), Revision: mem.CurrentRevision.Number,
			UpdatedAt: mem.CurrentRevision.CreatedAt,
		})
	}
	if len(items) > q.Limit {
		items = items[:q.Limit]
	}
	return items, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsKind(values []Kind, value Kind) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (r *fakeRepo) Recall(_ context.Context, q RecallQuery) ([]Hit, error) {
	if r.hits != nil {
		if q.Offset >= len(r.hits) {
			return nil, nil
		}
		end := min(len(r.hits), q.Offset+q.Limit)
		return r.hits[q.Offset:end], nil
	}
	var hits []Hit
	for _, mem := range r.memories {
		if mem.Status != StatusActive {
			continue
		}
		hits = append(hits, Hit{MemoryID: mem.ID, SpaceID: mem.SpaceID,
			Title: mem.CurrentRevision.Title, Kind: mem.CurrentRevision.Kind,
			Topics: mem.CurrentRevision.Topics, Excerpt: excerpt(mem.CurrentRevision.Body, 240),
			Revision: mem.CurrentRevision.Number, Score: 1})
	}
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

func (r *fakeRepo) Catalog(context.Context, string, []string, []WorkspaceClaim) ([]CatalogEntry, error) {
	if r.catalog != nil {
		return r.catalog, nil
	}
	return []CatalogEntry{{SpaceID: "project", Name: "Project", Slug: "project", Type: "project", Topics: []string{"go"}}}, nil
}

func (r *fakeRepo) ResolveWorkspace(context.Context, []WorkspaceClaim) ([]string, error) {
	return []string{"project"}, nil
}
