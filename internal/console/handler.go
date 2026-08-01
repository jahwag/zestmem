package console

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/memory"
)

type Service interface {
	Catalog(context.Context, memory.Principal, []memory.WorkspaceClaim, ...string) (memory.CatalogResult, error)
	BrowsePage(context.Context, memory.BrowseQuery) (memory.BrowseResult, error)
	RecallPage(context.Context, memory.RecallQuery) (memory.RecallResult, error)
	Read(context.Context, memory.Principal, string) (memory.Memory, error)
	DiscoverMetadata(context.Context, memory.MetadataQuery) (memory.MetadataResult, error)
}

type filterRequest struct {
	Statuses               []memory.Status `json:"statuses,omitempty"`
	Revision               *int            `json:"revision,omitempty"`
	ProvenanceClasses      []string        `json:"provenance_classes,omitempty"`
	ProvenanceLocators     []string        `json:"provenance_locators,omitempty"`
	ProvenanceAttributions []string        `json:"provenance_attributions,omitempty"`
	RememberedFrom         *time.Time      `json:"remembered_from,omitempty"`
	RememberedTo           *time.Time      `json:"remembered_to,omitempty"`
	RevisedFrom            *time.Time      `json:"revised_from,omitempty"`
	RevisedTo              *time.Time      `json:"revised_to,omitempty"`
	ValidFrom              *time.Time      `json:"valid_from,omitempty"`
	ValidTo                *time.Time      `json:"valid_to,omitempty"`
}

func (f filterRequest) domain() memory.MemoryFilters {
	return memory.MemoryFilters{
		Statuses: f.Statuses, Revision: f.Revision, ProvenanceClasses: f.ProvenanceClasses,
		ProvenanceLocators: f.ProvenanceLocators, ProvenanceAttributions: f.ProvenanceAttributions,
		RememberedFrom: f.RememberedFrom, RememberedTo: f.RememberedTo, RevisedFrom: f.RevisedFrom,
		RevisedTo: f.RevisedTo, ValidFrom: f.ValidFrom, ValidTo: f.ValidTo,
	}
}

type browseRequest struct {
	filterRequest
	SpaceIDs []string      `json:"space_ids,omitempty"`
	Kinds    []memory.Kind `json:"kinds,omitempty"`
	Topics   []string      `json:"topics,omitempty"`
	Limit    int           `json:"limit,omitempty"`
	Cursor   string        `json:"cursor,omitempty"`
}

//go:embed assets/*
var assets embed.FS

type recallRequest struct {
	filterRequest
	Query    string        `json:"query"`
	SpaceIDs []string      `json:"space_ids,omitempty"`
	Kinds    []memory.Kind `json:"kinds,omitempty"`
	Topics   []string      `json:"topics,omitempty"`
	Limit    int           `json:"limit,omitempty"`
	Cursor   string        `json:"cursor,omitempty"`
}

type metadataRequest struct {
	Facet    memory.MetadataFacet `json:"facet"`
	Query    string               `json:"query,omitempty"`
	SpaceIDs []string             `json:"space_ids,omitempty"`
	Statuses []memory.Status      `json:"statuses,omitempty"`
	Limit    int                  `json:"limit,omitempty"`
	Cursor   string               `json:"cursor,omitempty"`
}

func Handler(domain Service, protect func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /console/", http.StripPrefix("/console/", http.FileServerFS(static)))
	mux.Handle("GET /api/v1/catalog", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		result, err := domain.Catalog(r.Context(), principal, nil, r.URL.Query().Get("cursor"))
		writeJSON(w, result, err)
	})))
	mux.Handle("POST /api/v1/metadata", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		var input metadataRequest
		if err := decodeRequest(w, r, &input); err != nil {
			writeError(w, memory.ErrInvalidInput)
			return
		}
		result, err := domain.DiscoverMetadata(r.Context(), memory.MetadataQuery{Facet: input.Facet, Query: input.Query, SpaceIDs: input.SpaceIDs, Statuses: input.Statuses, Limit: input.Limit, Cursor: input.Cursor, Principal: principal})
		writeJSON(w, result, err)
	})))
	mux.Handle("POST /api/v1/browse", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		var input browseRequest
		if err := decodeRequest(w, r, &input); err != nil {
			writeError(w, memory.ErrInvalidInput)
			return
		}
		result, err := domain.BrowsePage(r.Context(), memory.BrowseQuery{
			MemoryFilters: input.filterRequest.domain(),
			SpaceIDs:      input.SpaceIDs, Kinds: input.Kinds, Topics: input.Topics,
			Limit: input.Limit, Cursor: input.Cursor, Principal: principal,
		})
		writeJSON(w, result, err)
	})))
	mux.Handle("POST /api/v1/recall", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		var input recallRequest
		if err := decodeRequest(w, r, &input); err != nil || strings.TrimSpace(input.Query) == "" {
			writeError(w, memory.ErrInvalidInput)
			return
		}
		result, err := domain.RecallPage(r.Context(), memory.RecallQuery{
			MemoryFilters: input.filterRequest.domain(),
			Query:         strings.TrimSpace(input.Query), SpaceIDs: input.SpaceIDs, Kinds: input.Kinds,
			Topics: input.Topics, Limit: input.Limit, Cursor: input.Cursor, Principal: principal,
		})
		writeJSON(w, result, err)
	})))
	mux.Handle("GET /api/v1/memories/{id}", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		result, err := domain.Read(r.Context(), principal, r.PathValue("id"))
		writeJSON(w, result, err)
	})))
	return securityHeaders(mux)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "operation failed"
	switch {
	case errors.Is(err, memory.ErrInvalidInput):
		status, message = http.StatusBadRequest, "invalid request"
	case errors.Is(err, memory.ErrNotFound):
		status, message = http.StatusNotFound, "memory not found"
	case errors.Is(err, memory.ErrForbidden):
		status, message = http.StatusForbidden, "forbidden"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
