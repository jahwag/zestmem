package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/console"
	"github.com/bytelope/zestmem/internal/memory"
)

const (
	alphaSpace = "10000000-0000-0000-0000-000000000001"
	betaSpace  = "20000000-0000-0000-0000-000000000002"
)

type fixtureService struct{}

func (fixtureService) DiscoverMetadata(ctx context.Context, query memory.MetadataQuery) (memory.MetadataResult, error) {
	if query.Query == "slow" {
		select {
		case <-time.After(750 * time.Millisecond):
		case <-ctx.Done():
			return memory.MetadataResult{}, ctx.Err()
		}
	}
	values := map[memory.MetadataFacet][]memory.MetadataValue{
		memory.MetadataTopic:                 {{Value: "security", MemoryCount: 3}, {Value: "architecture", MemoryCount: 1}},
		memory.MetadataProvenanceClass:       {{Value: "git_commit", MemoryCount: 2}},
		memory.MetadataProvenanceLocator:     {{Value: "architecture review", MemoryCount: 1}},
		memory.MetadataProvenanceAttribution: {{Value: "Avery User", MemoryCount: 1}},
	}
	if query.Facet == memory.MetadataTopic && query.Query == "" {
		if query.Cursor == "topic-page-2" {
			return memory.MetadataResult{Values: values[query.Facet][1:], TotalCount: 2}, nil
		}
		return memory.MetadataResult{Values: values[query.Facet][:1], TotalCount: 2, NextCursor: "topic-page-2"}, nil
	}
	items := values[query.Facet]
	if query.Query != "" && query.Query != "slow" {
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Value), strings.ToLower(query.Query)) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return memory.MetadataResult{Values: items, TotalCount: len(items)}, nil
}

func (fixtureService) Catalog(_ context.Context, _ memory.Principal, _ []memory.WorkspaceClaim, cursor ...string) (memory.CatalogResult, error) {
	if len(cursor) > 0 && cursor[0] == "page-2" {
		return memory.CatalogResult{Spaces: []memory.CatalogEntry{{
			SpaceID: betaSpace, Name: "Beta Space", Slug: "beta", Type: "project", Topics: []string{"security"},
		}}}, nil
	}
	return memory.CatalogResult{
		Spaces: []memory.CatalogEntry{{
			SpaceID: alphaSpace, Name: "Alpha Space", Slug: "alpha", Type: "project", Topics: []string{"architecture", "security"},
		}}, OmittedCount: 1, NextCursor: "page-2",
	}, nil
}

func (fixtureService) BrowsePage(_ context.Context, query memory.BrowseQuery) (memory.BrowseResult, error) {
	if len(query.Statuses) == 1 && query.Statuses[0] == memory.StatusRetracted {
		return memory.BrowseResult{}, nil
	}
	if query.Revision != nil && *query.Revision != 2 {
		return memory.BrowseResult{}, nil
	}
	if len(query.ProvenanceClasses) > 0 && query.ProvenanceClasses[0] != "git_commit" {
		return memory.BrowseResult{}, nil
	}
	if len(query.ProvenanceLocators) > 0 && query.ProvenanceLocators[0] != "architecture review" {
		return memory.BrowseResult{}, nil
	}
	if len(query.ProvenanceAttributions) > 0 && query.ProvenanceAttributions[0] != "Avery User" {
		return memory.BrowseResult{}, nil
	}
	if query.Cursor == "browse-2" {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-3", alphaSpace, "Old deployment lesson", memory.KindLesson, "rollback", time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC))}, TotalCount: 3}, nil
	}
	if len(query.SpaceIDs) == 1 && query.SpaceIDs[0] == betaSpace {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-4", betaSpace, "Beta security boundary", memory.KindDirective, "credentials", time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC))}, TotalCount: 1}, nil
	}
	if len(query.SpaceIDs) == 1 && query.SpaceIDs[0] == alphaSpace {
		items := make([]memory.BrowseItem, 20)
		for i := range items {
			title := fmt.Sprintf("Alpha memory %02d", i+1)
			id := fmt.Sprintf("alpha-memory-%02d", i+1)
			items[i] = browseItem(id, alphaSpace, title, memory.KindFact, "A memory in the Alpha Space.", time.Date(2026, 7, 20, 12-i, 0, 0, 0, time.UTC))
		}
		return memory.BrowseResult{Items: items, TotalCount: 20}, nil
	}
	if len(query.Kinds) == 1 && query.Kinds[0] == memory.KindLesson {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-3", alphaSpace, "Old deployment lesson", memory.KindLesson, "rollback", time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC))}, TotalCount: 1}, nil
	}
	return memory.BrowseResult{
		Items: []memory.BrowseItem{
			browseItem("memory-1", alphaSpace, "Keep HTML inert", memory.KindDecision, "Render memory bodies as text.", time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)),
			browseItem("memory-2", betaSpace, "Reader keys only", memory.KindDirective, "Use a dedicated reader principal.", time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)),
		},
		TotalCount: 3, NextCursor: "browse-2",
	}, nil
}

func (fixtureService) RecallPage(_ context.Context, query memory.RecallQuery) (memory.RecallResult, error) {
	if query.Revision != nil && *query.Revision != 2 {
		return memory.RecallResult{}, nil
	}
	if len(query.ProvenanceClasses) > 0 && query.ProvenanceClasses[0] != "git_commit" {
		return memory.RecallResult{}, nil
	}
	if query.Query != "inert html" || len(query.Kinds) > 0 || len(query.Topics) > 0 ||
		(len(query.SpaceIDs) == 1 && query.SpaceIDs[0] != alphaSpace) {
		return memory.RecallResult{}, nil
	}
	return memory.RecallResult{Hits: []memory.Hit{{
		MemoryID: "memory-1", SpaceID: alphaSpace, Title: "Keep HTML inert", Kind: memory.KindDecision,
		Topics: []string{"security"}, Excerpt: "Render memory bodies as text.", Revision: 2, Score: 0.032,
	}}, TotalCount: 1}, nil
}

func (fixtureService) Read(_ context.Context, _ memory.Principal, id string) (memory.Memory, error) {
	title := map[string]string{
		"memory-1": "Keep HTML inert", "memory-2": "Reader keys only", "memory-3": "Old deployment lesson", "memory-4": "Beta security boundary",
	}[id]
	if strings.HasPrefix(id, "alpha-memory-") {
		title = "Alpha memory " + strings.TrimPrefix(id, "alpha-memory-")
	}
	if title == "" {
		return memory.Memory{}, memory.ErrNotFound
	}
	spaceID := alphaSpace
	if id == "memory-2" || id == "memory-4" {
		spaceID = betaSpace
	}
	kind := memory.KindDecision
	switch {
	case id == "memory-2" || id == "memory-4":
		kind = memory.KindDirective
	case id == "memory-3":
		kind = memory.KindLesson
	case strings.HasPrefix(id, "alpha-memory-"):
		kind = memory.KindFact
	}
	var associations []memory.HistoricalAssociation
	if id == "memory-1" {
		associations = []memory.HistoricalAssociation{{
			Relationship: "intended_agent", PrincipalID: "athena-principal", PrincipalDisplayName: "Athena",
			BasisRevision: 1, BasisClass: "legacy_role_path", BasisLocator: "worker/athena",
			AssertedBy: "migration-operator", AssertedByDisplayName: "Migration Operator",
			AssertedAt: time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC), Reason: "Legacy worker collection mapping",
		}}
	} else if id == "alpha-memory-20" {
		associations = []memory.HistoricalAssociation{{
			AssertionID: "assertion-" + strings.Repeat("a", 180), Relationship: "intended_agent",
			PrincipalID: "athena-" + strings.Repeat("p", 180), PrincipalDisplayName: "Athena",
			BasisRevision: 1, BasisClass: "legacy_role_path", BasisLocator: strings.Repeat("worker/very-long-segment/", 70),
			AssertedBy: "migration-operator-" + strings.Repeat("o", 180), AssertedByDisplayName: "Migration Operator",
			AssertedAt: time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC), Reason: strings.Repeat("Reviewed legacy mapping evidence. ", 40),
		}}
	}
	return memory.Memory{
		ID: id, SpaceID: spaceID, Status: memory.StatusActive, CreatedAt: time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC),
		HistoricalAssociations: associations,
		CurrentRevision: memory.Revision{
			MemoryID: id, Number: 2, Title: title, Body: "Literal body: <img src=x onerror=window.hacked=true>",
			Kind: kind, Topics: []string{"security", "ui"},
			Provenance: []memory.Provenance{
				{Class: "git_commit", Locator: "architecture review", Attribution: "Avery User"},
				{Class: "long_exact_provenance_class_token_that_must_wrap_without_being_clipped", Locator: "mobile layout review"},
			},
			ActorID: "migration-operator", ActorDisplayName: "Migration Operator", CreatedAt: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), Reason: "Prevent active content in durable memories",
		},
	}, nil
}

func browseItem(id, spaceID, title string, kind memory.Kind, excerpt string, updatedAt time.Time) memory.BrowseItem {
	return memory.BrowseItem{
		MemoryID: id, SpaceID: spaceID, Title: title, Kind: kind, Topics: []string{"security"},
		Excerpt: excerpt, Revision: 2, UpdatedAt: updatedAt,
	}
}

func protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if credential != "zm_test_reader" && credential != "zm_test_second" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		principal := memory.Principal{ID: credential, Type: "human"}
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), principal)))
	})
}

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:4173", console.Handler(fixtureService{}, protect)))
}
