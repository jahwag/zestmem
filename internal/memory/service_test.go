package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

func TestStartupCatalogRetainsEntriesBeyondBoundedCatalogPage(t *testing.T) {
	svc, repo, _ := testService()
	repo.catalog = make([]CatalogEntry, 100)
	for i := range repo.catalog {
		repo.catalog[i] = CatalogEntry{SpaceID: fmt.Sprintf("space-%03d", i), Name: strings.Repeat("name", 20), Slug: fmt.Sprintf("space-%03d", i), Type: "project"}
	}
	page, err := svc.Catalog(context.Background(), Principal{ID: "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	startupCatalog, err := svc.StartupCatalog(context.Background(), Principal{ID: "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Spaces) >= len(repo.catalog) || len(startupCatalog.Spaces) != len(repo.catalog) {
		t.Fatalf("page=%d startup=%d total=%d", len(page.Spaces), len(startupCatalog.Spaces), len(repo.catalog))
	}
}

func TestCatalogMaximumEscapedSpaceCueStillAdvances(t *testing.T) {
	svc, repo, _ := testService()
	repo.catalog = []CatalogEntry{
		{SpaceID: strings.Repeat("i", 36), Name: strings.Repeat("n", 100), Slug: strings.Repeat("s", 80), Type: "project", UseWhen: strings.Repeat("<&>", 80), Topics: []string{strings.Repeat("t", 80)}},
		{SpaceID: strings.Repeat("j", 36), Name: "next", Slug: "next", Type: "project"},
	}
	page, err := svc.Catalog(context.Background(), Principal{ID: "agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if len(page.Spaces) == 0 || conservativeTokenCount(raw) > 800 {
		t.Fatalf("page=%#v bytes=%d", page, len(raw))
	}
	if page.NextCursor != "" {
		next, err := svc.Catalog(context.Background(), Principal{ID: "agent"}, nil, page.NextCursor)
		if err != nil || len(next.Spaces) == 0 {
			t.Fatalf("next=%#v error=%v", next, err)
		}
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
	if len(first.Hits) != 3 || first.TotalCount != 7 || first.NextCursor == "" {
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

func TestRecallCompactsAggregateBeforeDroppingRankedHits(t *testing.T) {
	svc, repo, _ := testService()
	useWhen := strings.Repeat("u", 200) + " without using stale guidance."
	for i := 0; i < 4; i++ {
		repo.hits = append(repo.hits, Hit{
			MemoryID: fmt.Sprintf("m-%d", i), SpaceID: "project",
			Title: strings.Repeat("t", 37), UseWhen: useWhen,
			Excerpt: strings.Repeat("e", 400), Kind: KindFact, Revision: 1, Score: 1,
		})
	}
	first, err := svc.RecallPage(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "task", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hits) != 3 || first.NextCursor == "" {
		t.Fatalf("first=%#v", first)
	}
	for i, hit := range first.Hits {
		if hit.MemoryID != fmt.Sprintf("m-%d", i) {
			t.Fatalf("rank %d=%q", i, hit.MemoryID)
		}
		if hit.UseWhen != useWhen {
			t.Fatalf("rank %d cue was semantically truncated: %q", i, hit.UseWhen)
		}
	}
	raw, _ := json.Marshal(first)
	if tokens := conservativeTokenCount(raw); tokens > 2000 {
		t.Fatalf("tokens=%d", tokens)
	}
	second, err := svc.RecallPage(context.Background(), RecallQuery{Principal: Principal{ID: "agent"}, Query: "task", Limit: 3, Cursor: first.NextCursor})
	if err != nil || len(second.Hits) != 1 || second.Hits[0].MemoryID != "m-3" {
		t.Fatalf("second=%#v err=%v", second, err)
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
	if len(first.Items) != 2 || first.TotalCount != 3 || first.NextCursor == "" || repo.browseQuery.SpaceIDs[0] != "project" || len(repo.browseQuery.SpaceIDs) != 1 {
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

func TestRememberRequiresIntentionalNewTopics(t *testing.T) {
	svc, repo, _ := testService()
	repo.topicUsage = map[string]int{}
	repo.metadata = RepositoryMetadataResult{Values: []MetadataValue{{Value: "architecture", MemoryCount: 9}}, TotalCount: 1}
	cmd := validRemember()
	result, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrUnfamiliarTopics) || result.Written || len(repo.memories) != 0 {
		t.Fatalf("result=%#v err=%v memories=%d", result, err, len(repo.memories))
	}
	if len(result.UnfamiliarTopics) != 2 || result.TopicSuggestions[0].Value != "architecture" {
		t.Fatalf("topic advice=%#v", result)
	}
	cmd.ConfirmNewTopics = result.UnfamiliarTopics
	result, err = svc.Remember(context.Background(), cmd)
	if err != nil || !result.Written || len(result.UnfamiliarTopics) != 2 {
		t.Fatalf("confirmed result=%#v err=%v", result, err)
	}
}

func TestRememberConfirmationMustMatchCurrentUnfamiliarTopics(t *testing.T) {
	svc, repo, _ := testService()
	repo.topicUsage = map[string]int{}
	cmd := validRemember()
	cmd.ConfirmNewTopics = []string{"api design"}
	result, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrUnfamiliarTopics) || result.Written || len(repo.memories) != 0 {
		t.Fatalf("partial confirmation wrote: result=%#v err=%v", result, err)
	}
	cmd.Topics = []string{"different topic"}
	result, err = svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrUnfamiliarTopics) || result.Written || len(repo.memories) != 0 {
		t.Fatalf("stale confirmation wrote: result=%#v err=%v", result, err)
	}
}

func TestReadSegmentPagesAndGrepsWithoutReturningFullBody(t *testing.T) {
	svc, repo, _ := testService()
	cmd := validRemember()
	cmd.Topics = nil
	cmd.Body = "one\nneedle two\nthree\nfour\nneedle five\nsix"
	cmd.Provenance = []Provenance{{Class: "user_instruction", Locator: strings.Repeat("source", 100), Attribution: "Avery"}}
	validUntil := time.Now().Add(time.Hour)
	cmd.ValidUntil = &validUntil
	created, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	window, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, StartLine: 2, LineCount: 2})
	if err != nil || window.Content != "needle two\nthree" || window.UseWhen != cmd.UseWhen || window.Window.NextStartLine != 4 || len(window.Provenance) != 1 || !window.Provenance[0].LocatorTruncated || window.Provenance[0].Attribution != "Avery" || window.ValidUntil == nil || window.Reason == "" {
		t.Fatalf("window=%#v err=%v", window, err)
	}
	matches, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, Pattern: "needle", ContextLines: 1})
	if err != nil || matches.UseWhen != cmd.UseWhen || len(matches.Matches) != 1 || matches.Matches[0].StartLine != 1 || matches.Matches[0].EndLine != 6 {
		t.Fatalf("matches=%#v err=%v", matches, err)
	}
	if repo.memories[created.Memory.ID].CurrentRevision.Body == "" {
		t.Fatal("bounded read mutated stored memory")
	}
}

func TestReadSegmentByteCeilingHandlesOneLongUTF8Line(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.Topics = nil
	cmd.Body = strings.Repeat("é", 3000)
	created, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID})
	if err != nil || len(first.Content) > 4096 || first.Window.NextStartByte == 0 {
		t.Fatalf("first=%#v err=%v", first.Window, err)
	}
	second, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, StartByte: first.Window.NextStartByte})
	if err != nil || first.Content+second.Content != cmd.Body {
		t.Fatalf("second=%#v err=%v reconstructed=%d want=%d", second.Window, err, len(first.Content+second.Content), len(cmd.Body))
	}
}

func TestReadSegmentReportsNewlineAndGrepTruncationPrecisely(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.Topics = nil
	cmd.Body = "a\nneedle one\nx\nneedle two\ny\nneedle three"
	created, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	window, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, MaxBytes: 4})
	if err != nil || window.Content != "a\nne" || window.Window.EndLine != 2 || window.Window.NextStartByte != 4 {
		t.Fatalf("newline window=%#v err=%v", window, err)
	}
	matches, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, Pattern: "needle", MaxMatches: 1})
	if err != nil || matches.TotalMatchGroups != 3 || matches.OmittedMatchGroups != 2 || matches.NextMatchStartLine != 4 {
		t.Fatalf("grep truncation=%#v err=%v", matches, err)
	}
}

func TestReadSegmentProvidesByteContinuationForTruncatedMatch(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.Topics = nil
	cmd.Body = strings.Repeat("needle", 1000)
	created, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: cmd.Principal, MemoryID: created.Memory.ID, Pattern: "needle", MaxBytes: 64})
	if err != nil || len(matches.Matches) != 1 || !matches.Matches[0].Truncated || matches.NextMatchStartByte != 64 || matches.OmittedMatchGroups != 0 {
		t.Fatalf("truncated match=%#v err=%v", matches, err)
	}
}

func TestMetadataDiscoveryCursorIsBoundToFacetAndAuthorization(t *testing.T) {
	svc, repo, auth := testService()
	auth.spaces = []string{"project"}
	repo.metadata = RepositoryMetadataResult{Values: []MetadataValue{{Value: "go", MemoryCount: 3}}, TotalCount: 2}
	first, err := svc.DiscoverMetadata(context.Background(), MetadataQuery{Facet: MetadataTopic, Principal: Principal{ID: "agent"}, SpaceIDs: []string{"project", "forbidden"}, Limit: 1})
	if err != nil || first.TotalCount != 2 || first.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	_, err = svc.DiscoverMetadata(context.Background(), MetadataQuery{Facet: MetadataProvenanceClass, Principal: Principal{ID: "agent"}, Limit: 1, Cursor: first.NextCursor})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-facet cursor error=%v", err)
	}
}

func TestMetadataDiscoveryHasSerializedResponseBudget(t *testing.T) {
	svc, repo, _ := testService()
	repo.metadata.TotalCount = 100
	for i := 0; i < 100; i++ {
		repo.metadata.Values = append(repo.metadata.Values, MetadataValue{Value: fmt.Sprintf("%03d-%s", i, strings.Repeat("locator", 280)), MemoryCount: 1})
	}
	page, err := svc.DiscoverMetadata(context.Background(), MetadataQuery{Facet: MetadataProvenanceLocator, Principal: Principal{ID: "agent"}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if len(raw) > 13<<10 || len(page.Values) >= 100 || page.NextCursor == "" {
		t.Fatalf("metadata response bytes=%d values=%d cursor=%q", len(raw), len(page.Values), page.NextCursor)
	}
	seen := len(page.Values)
	for page.NextCursor != "" {
		page, err = svc.DiscoverMetadata(context.Background(), MetadataQuery{Facet: MetadataProvenanceLocator, Principal: Principal{ID: "agent"}, Limit: 100, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Values)
	}
	if seen != 100 {
		t.Fatalf("paged metadata values=%d", seen)
	}
}

func TestMetadataDiscoveryDoesNotReturnNonAdvancingCursor(t *testing.T) {
	svc, repo, _ := testService()
	repo.metadata = RepositoryMetadataResult{Values: []MetadataValue{}, TotalCount: 2}
	page, err := svc.DiscoverMetadata(context.Background(), MetadataQuery{Facet: MetadataTopic, Principal: Principal{ID: "agent"}})
	if err != nil || page.NextCursor != "" {
		t.Fatalf("empty metadata page=%#v err=%v", page, err)
	}
}

func TestRememberNormalizesUseWhen(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.UseWhen = "  Debugging  cafe\u0301   connection failures.  "
	result, err := svc.Remember(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Memory.CurrentRevision.UseWhen, "Debugging café connection failures."; got != want {
		t.Fatalf("use_when=%q, want %q", got, want)
	}
}

func TestRememberRejectsUnsafeUseWhen(t *testing.T) {
	tests := map[string]string{
		"newline":        "Debugging pool\nfailures",
		"line separator": "Debugging pool\u2028failures",
		"control":        "Debugging pool\tfailures",
		"bidi override":  "Debugging pool \u202efailures",
		"formatting":     "Debugging pool \u200bfailures",
		"noncharacter":   "Debugging pool \ufdd0failures",
		"too long":       strings.Repeat("x", MaxUseWhenBytes+1),
		"invalid UTF-8":  string([]byte{'x', 0xff}),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := testService()
			cmd := validRemember()
			cmd.UseWhen = value
			_, err := svc.Remember(context.Background(), cmd)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestNormalizeReverieCue(t *testing.T) {
	normalized, err := NormalizeReverieCue("  diagnose   postgres pool failures  ")
	if err != nil || normalized != "diagnose postgres pool failures" {
		t.Fatalf("normalized=%q err=%v", normalized, err)
	}
	for name, cue := range map[string]string{
		"too short":      "pool failures",
		"too long":       "one two three four five six seven eight nine ten eleven twelve thirteen",
		"unsafe control": "diagnose postgres\x00 failures",
		"invalid UTF-8":  string([]byte{'a', 'b', 'c', ' ', 0xff, ' ', 'x'}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeReverieCue(cue); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReverieAndReadsRecordAuthenticatedRetrievals(t *testing.T) {
	svc, repo, _ := testService()
	recorder := &fakeRetrievalRecorder{}
	svc.WithRetrievalRecorder(recorder)
	for i := 0; i < 7; i++ {
		repo.hits = append(repo.hits, Hit{
			MemoryID: "memory-" + strconv.Itoa(i),
			SpaceID:  "project",
			Title:    "Connection guidance",
			UseWhen:  "Diagnosing postgres pool failures.",
			Kind:     KindFact,
			Revision: 2,
			Score:    1,
		})
	}

	result, err := svc.Reverie(context.Background(), Principal{ID: "athena"}, " diagnose  postgres pool failures ")
	if err != nil {
		t.Fatal(err)
	}
	if result.Cue != "diagnose postgres pool failures" || len(result.Reveries) != 5 || result.TotalCount != 7 {
		t.Fatalf("reverie=%#v", result)
	}
	if !slices.Contains(result.Reveries[0].MatchReasons, "use_when") {
		t.Fatalf("match reasons=%v", result.Reveries[0].MatchReasons)
	}
	if len(recorder.events) != 1 || recorder.events[0].Operation != RetrievalReverie || recorder.events[0].PrincipalID != "athena" || recorder.events[0].Cue != result.Cue || len(recorder.events[0].Targets) != 5 {
		t.Fatalf("events=%#v", recorder.events)
	}

	repo.hits = nil
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(context.Background(), Principal{ID: "agent"}, created.Memory.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, Pattern: "["}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid segment error=%v", err)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("invalid segment recorded retrieval: %#v", recorder.events)
	}
	if _, err := svc.ReadSegment(context.Background(), ReadQuery{Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 3 || recorder.events[1].Operation != RetrievalRead || recorder.events[2].Operation != RetrievalReadSegment {
		t.Fatalf("events=%#v", recorder.events)
	}
}

func TestRememberUseWhenParticipatesInIdempotency(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.UseWhen = "Debugging database connection failures."
	if _, err := svc.Remember(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	cmd.UseWhen = "Reviewing database connection limits."
	_, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("error=%v, want ErrIdempotencyReuse", err)
	}
}

func TestMissingUseWhenPreservesLegacyIdempotencyHash(t *testing.T) {
	svc, repo, _ := testService()
	cmd := validRemember()
	cmd.UseWhen = ""
	if _, err := svc.Remember(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	want := requestHash("remember", cmd.SpaceID, contentHash(cmd.Title, cmd.Body, cmd.Kind), cmd.Reason,
		mutationMetadataHash(normalizeTopics(cmd.Topics), []Provenance{{Class: "unverified"}}, nil))
	if got := repo.requestHashes[repo.idemKey(cmd.Principal.ID, "remember", cmd.IdempotencyKey)]; got != want {
		t.Fatalf("legacy request hash changed: got=%s want=%s", got, want)
	}
}

func TestUseWhenDoesNotChangeContentIdentity(t *testing.T) {
	svc, _, _ := testService()
	cmd := validRemember()
	cmd.UseWhen = "Debugging database connection failures."
	if _, err := svc.Remember(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	cmd.UseWhen = "Reviewing database connection limits."
	cmd.IdempotencyKey = "create-2"
	_, err := svc.Remember(context.Background(), cmd)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("error=%v, want ErrDuplicate", err)
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
	repo.topicUsage = map[string]int{}
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
		UseWhen: created.Memory.CurrentRevision.UseWhen, UseWhenSet: true,
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
		UseWhen: "Reviewing updated behavior.", UseWhenSet: true, Kind: KindFact, Reason: "Correct wording", IdempotencyKey: "rev-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Memory.CurrentRevision.Number != 2 || result.Memory.CurrentRevision.Body != "Updated body" || result.Memory.CurrentRevision.UseWhen != "Reviewing updated behavior." {
		t.Fatalf("revision = %#v", result.Memory.CurrentRevision)
	}
}

func TestReviseRetryReturnsOriginalResultAfterRevisionAdvanced(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	cmd := ReviseCommand{Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, ExpectedRevision: 1, Title: "Updated", Body: "Updated body", UseWhen: created.Memory.CurrentRevision.UseWhen, UseWhenSet: true, Kind: KindFact, Reason: "Correct wording", IdempotencyKey: "rev-retry"}
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

type rejectingEmbedder struct{ calls int }

func (e *rejectingEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	return nil, errors.New("embedder must not be called")
}

func TestCueOnlyRevisionReusesEmbeddingAndPreservesSubstantiveRecency(t *testing.T) {
	svc, repo, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	rejecting := &rejectingEmbedder{}
	svc.WithEmbedder(rejecting)
	current := created.Memory.CurrentRevision
	result, err := svc.Revise(context.Background(), ReviseCommand{
		Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, ExpectedRevision: 1,
		Title: current.Title, Body: current.Body, UseWhen: "Debugging the relevant fact.", UseWhenSet: true,
		Kind: current.Kind, Topics: current.Topics, Provenance: current.Provenance,
		Reason: "Add reviewed retrieval cue", IdempotencyKey: "cue-only-revision",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejecting.calls != 0 {
		t.Fatalf("embedder calls=%d", rejecting.calls)
	}
	if result.Memory.CurrentRevision.Number != 2 || repo.lastRevise.ReuseEmbeddingRevision != 1 || !repo.lastRevise.PreserveContentUpdatedAt || len(repo.lastRevise.Embedding) != 0 {
		t.Fatalf("result=%#v revise=%#v", result, repo.lastRevise)
	}
}

func TestSubstantiveRevisionRequiresExplicitCueDecision(t *testing.T) {
	svc, _, _ := testService()
	created, err := svc.Remember(context.Background(), validRemember())
	if err != nil {
		t.Fatal(err)
	}
	current := created.Memory.CurrentRevision
	_, err = svc.Revise(context.Background(), ReviseCommand{
		Principal: Principal{ID: "agent"}, MemoryID: created.Memory.ID, ExpectedRevision: 1,
		Title: current.Title, Body: "Changed body", Kind: current.Kind, Topics: current.Topics, Provenance: current.Provenance,
		Reason: "Substantive correction", IdempotencyKey: "missing-cue-decision",
	})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "explicitly preserved") {
		t.Fatalf("error=%v", err)
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
		Title: "A fact", Body: "Useful body", UseWhen: "Investigating a relevant fact.", Kind: KindFact,
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

type fakeRetrievalRecorder struct {
	events []RetrievalEvent
	err    error
}

func (r *fakeRetrievalRecorder) RecordRetrieval(_ context.Context, event RetrievalEvent) error {
	r.events = append(r.events, event)
	return r.err
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
	metadata      RepositoryMetadataResult
	topicUsage    map[string]int
	auth          Authorizer
	lastRevise    ReviseRecord
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
	rev := Revision{MemoryID: rec.NewID, Number: 1, Title: rec.Title, Body: rec.Body, UseWhen: rec.UseWhen, Kind: rec.Kind,
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
	r.lastRevise = rec
	mem := r.memories[rec.MemoryID]
	if mem.CurrentRevision.Number != rec.ExpectedRevision {
		return Memory{}, ErrConflict
	}
	mem.CurrentRevision = Revision{MemoryID: rec.MemoryID, Number: rec.ExpectedRevision + 1,
		Title: rec.Title, Body: rec.Body, UseWhen: rec.UseWhen, Kind: rec.Kind, Topics: rec.Topics,
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

func (r *fakeRepo) Browse(_ context.Context, q BrowseQuery) (RepositoryBrowseResult, error) {
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
			return RepositoryBrowseResult{TotalCount: len(r.browse)}, nil
		}
		end := min(len(r.browse), start+q.Limit)
		return RepositoryBrowseResult{Items: r.browse[start:end], TotalCount: len(r.browse)}, nil
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
	return RepositoryBrowseResult{Items: items, TotalCount: len(items)}, nil
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

func (r *fakeRepo) Recall(_ context.Context, q RecallQuery) (RepositoryRecallResult, error) {
	if r.hits != nil {
		if q.Offset >= len(r.hits) {
			return RepositoryRecallResult{TotalCount: len(r.hits)}, nil
		}
		end := min(len(r.hits), q.Offset+q.Limit)
		return RepositoryRecallResult{Hits: r.hits[q.Offset:end], TotalCount: len(r.hits)}, nil
	}
	var hits []Hit
	for _, mem := range r.memories {
		if mem.Status != StatusActive {
			continue
		}
		hits = append(hits, Hit{MemoryID: mem.ID, SpaceID: mem.SpaceID,
			Title: mem.CurrentRevision.Title, Kind: mem.CurrentRevision.Kind,
			Topics: mem.CurrentRevision.Topics, UseWhen: mem.CurrentRevision.UseWhen, Excerpt: excerpt(mem.CurrentRevision.Body, 240),
			Revision: mem.CurrentRevision.Number, Score: 1})
	}
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return RepositoryRecallResult{Hits: hits, TotalCount: len(hits)}, nil
}

func (r *fakeRepo) Catalog(context.Context, string, []string, []WorkspaceClaim) ([]CatalogEntry, error) {
	if r.catalog != nil {
		return r.catalog, nil
	}
	return []CatalogEntry{{SpaceID: "project", Name: "Project", Slug: "project", Type: "project", Topics: []string{"go"}}}, nil
}

func (r *fakeRepo) DiscoverMetadata(_ context.Context, query MetadataQuery) (RepositoryMetadataResult, error) {
	if r.metadata.Values != nil || r.metadata.TotalCount != 0 {
		if query.Offset >= len(r.metadata.Values) {
			return RepositoryMetadataResult{TotalCount: r.metadata.TotalCount}, nil
		}
		end := min(len(r.metadata.Values), query.Offset+query.Limit)
		return RepositoryMetadataResult{Values: r.metadata.Values[query.Offset:end], TotalCount: r.metadata.TotalCount}, nil
	}
	return RepositoryMetadataResult{Values: []MetadataValue{{Value: "go", MemoryCount: 3}}, TotalCount: 1}, nil
}

func (r *fakeRepo) TopicUsage(_ context.Context, _, _ string, topics []string) (map[string]int, error) {
	if r.topicUsage != nil {
		return r.topicUsage, nil
	}
	usage := make(map[string]int, len(topics))
	for _, topic := range topics {
		usage[topic] = 1
	}
	return usage, nil
}

func (r *fakeRepo) ResolveWorkspace(context.Context, []WorkspaceClaim) ([]string, error) {
	return []string{"project"}, nil
}
