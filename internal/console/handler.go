package console

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/memory"
)

type Service interface {
	Catalog(context.Context, memory.Principal, []memory.WorkspaceClaim, ...string) (memory.CatalogResult, error)
	RecallPage(context.Context, memory.RecallQuery) (memory.RecallResult, error)
	Read(context.Context, memory.Principal, string) (memory.Memory, error)
}

//go:embed assets/*
var assets embed.FS

type recallRequest struct {
	Query    string        `json:"query"`
	SpaceIDs []string      `json:"space_ids,omitempty"`
	Kinds    []memory.Kind `json:"kinds,omitempty"`
	Topics   []string      `json:"topics,omitempty"`
	Limit    int           `json:"limit,omitempty"`
	Cursor   string        `json:"cursor,omitempty"`
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
	mux.Handle("POST /api/v1/recall", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, memory.ErrForbidden)
			return
		}
		var input recallRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Query) == "" {
			writeError(w, memory.ErrInvalidInput)
			return
		}
		result, err := domain.RecallPage(r.Context(), memory.RecallQuery{
			Query: strings.TrimSpace(input.Query), SpaceIDs: input.SpaceIDs, Kinds: input.Kinds,
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
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
