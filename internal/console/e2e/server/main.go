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
	if query.Cursor == "browse-2" {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-3", alphaSpace, "Old deployment lesson", memory.KindLesson, "rollback", time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC))}}, nil
	}
	if len(query.SpaceIDs) == 1 && query.SpaceIDs[0] == betaSpace {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-4", betaSpace, "Beta security boundary", memory.KindDirective, "credentials", time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC))}}, nil
	}
	if len(query.SpaceIDs) == 1 && query.SpaceIDs[0] == alphaSpace {
		items := make([]memory.BrowseItem, 20)
		for i := range items {
			title := fmt.Sprintf("Alpha memory %02d", i+1)
			id := fmt.Sprintf("alpha-memory-%02d", i+1)
			items[i] = browseItem(id, alphaSpace, title, memory.KindFact, "A memory in the Alpha Space.", time.Date(2026, 7, 20, 12-i, 0, 0, 0, time.UTC))
		}
		return memory.BrowseResult{Items: items}, nil
	}
	if len(query.Kinds) == 1 && query.Kinds[0] == memory.KindLesson {
		return memory.BrowseResult{Items: []memory.BrowseItem{browseItem("memory-3", alphaSpace, "Old deployment lesson", memory.KindLesson, "rollback", time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC))}}, nil
	}
	return memory.BrowseResult{
		Items: []memory.BrowseItem{
			browseItem("memory-1", alphaSpace, "Keep HTML inert", memory.KindDecision, "Render memory bodies as text.", time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)),
			browseItem("memory-2", betaSpace, "Reader keys only", memory.KindDirective, "Use a dedicated reader principal.", time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)),
		},
		NextCursor: "browse-2",
	}, nil
}

func (fixtureService) RecallPage(_ context.Context, query memory.RecallQuery) (memory.RecallResult, error) {
	if query.Query != "inert html" || len(query.Kinds) > 0 || len(query.Topics) > 0 ||
		(len(query.SpaceIDs) == 1 && query.SpaceIDs[0] != alphaSpace) {
		return memory.RecallResult{}, nil
	}
	return memory.RecallResult{Hits: []memory.Hit{{
		MemoryID: "memory-1", SpaceID: alphaSpace, Title: "Keep HTML inert", Kind: memory.KindDecision,
		Topics: []string{"security"}, Excerpt: "Render memory bodies as text.", Revision: 2, Score: 0.032,
	}}}, nil
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
	return memory.Memory{
		ID: id, SpaceID: spaceID, Status: memory.StatusActive, CreatedAt: time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC),
		CurrentRevision: memory.Revision{
			MemoryID: id, Number: 2, Title: title, Body: "Literal body: <img src=x onerror=window.hacked=true>",
			Kind: kind, Topics: []string{"security", "ui"},
			Provenance: []memory.Provenance{{Class: "verified", Locator: "architecture review"}},
			ActorID:    "agent", CreatedAt: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), Reason: "Prevent active content in durable memories",
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
		if r.Header.Get("Authorization") != "Bearer zm_test_reader" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		principal := memory.Principal{ID: "30000000-0000-0000-0000-000000000003", Type: "human"}
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), principal)))
	})
}

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:4173", console.Handler(fixtureService{}, protect)))
}
