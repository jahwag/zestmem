package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/memory"
)

type fakeService struct {
	catalog memory.CatalogResult
	recall  memory.RecallResult
	read    memory.Memory
	query   memory.RecallQuery
}

func (f *fakeService) Catalog(context.Context, memory.Principal, []memory.WorkspaceClaim, ...string) (memory.CatalogResult, error) {
	return f.catalog, nil
}
func (f *fakeService) RecallPage(_ context.Context, query memory.RecallQuery) (memory.RecallResult, error) {
	f.query = query
	return f.recall, nil
}
func (f *fakeService) Read(context.Context, memory.Principal, string) (memory.Memory, error) {
	return f.read, nil
}

func testProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		principal := memory.Principal{ID: "00000000-0000-0000-0000-000000000001", Type: "agent"}
		next.ServeHTTP(w, r.WithContext(authn.WithPrincipal(r.Context(), principal)))
	})
}

func TestConsoleStaticHasSecurityHeadersAndNoCredentialRequirement(t *testing.T) {
	handler := Handler(&fakeService{}, testProtect)
	request := httptest.NewRequest(http.MethodGet, "/console/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("strict CSP missing")
	}
	if !strings.Contains(response.Body.String(), "Open your memory bank") {
		t.Fatal("console HTML missing")
	}
}

func TestConsoleAPIDeniesMissingCredential(t *testing.T) {
	handler := Handler(&fakeService{}, testProtect)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRecallUsesAuthenticatedPrincipalAndFilters(t *testing.T) {
	service := &fakeService{recall: memory.RecallResult{Hits: []memory.Hit{{MemoryID: "m1", Title: "Safe rendering"}}}}
	handler := Handler(service, testProtect)
	body := `{"query":"  rendering constraints  ","space_ids":["s1"],"kinds":["decision"],"topics":["security"],"limit":12}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/recall", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.query.Query != "rendering constraints" || service.query.Principal.ID == "" || service.query.Limit != 12 {
		t.Fatalf("unexpected query: %#v", service.query)
	}
	var result memory.RecallResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Hits) != 1 {
		t.Fatalf("unexpected response: %v %#v", err, result)
	}
}

func TestRecallRejectsUnknownFields(t *testing.T) {
	handler := Handler(&fakeService{}, testProtect)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/recall", strings.NewReader(`{"query":"x","mutation":true}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}
