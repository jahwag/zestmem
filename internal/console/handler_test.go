package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/memory"
)

type fakeService struct {
	catalog     memory.CatalogResult
	browse      memory.BrowseResult
	recall      memory.RecallResult
	read        memory.Memory
	browseQuery memory.BrowseQuery
	query       memory.RecallQuery
	metadata    memory.MetadataQuery
}

func (f *fakeService) Catalog(context.Context, memory.Principal, []memory.WorkspaceClaim, ...string) (memory.CatalogResult, error) {
	return f.catalog, nil
}
func (f *fakeService) BrowsePage(_ context.Context, query memory.BrowseQuery) (memory.BrowseResult, error) {
	f.browseQuery = query
	return f.browse, nil
}
func (f *fakeService) RecallPage(_ context.Context, query memory.RecallQuery) (memory.RecallResult, error) {
	f.query = query
	return f.recall, nil
}
func (f *fakeService) DiscoverMetadata(_ context.Context, query memory.MetadataQuery) (memory.MetadataResult, error) {
	f.metadata = query
	return memory.MetadataResult{Values: []memory.MetadataValue{{Value: "security", MemoryCount: 2}}, TotalCount: 1}, nil
}

func TestBrowseUsesAuthenticatedPrincipalAndFilters(t *testing.T) {
	service := &fakeService{browse: memory.BrowseResult{Items: []memory.BrowseItem{{MemoryID: "m1", Title: "Recent memory"}}}}
	handler := Handler(service, testProtect)
	body := `{"space_ids":["s1"],"kinds":["decision"],"topics":["security"],"statuses":["active"],"revision":2,"provenance_classes":["verified"],"remembered_from":"2026-07-01T00:00:00Z","limit":24}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/browse", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if service.browseQuery.Principal.ID == "" || service.browseQuery.Limit != 24 || service.browseQuery.SpaceIDs[0] != "s1" || *service.browseQuery.Revision != 2 || service.browseQuery.ProvenanceClasses[0] != "verified" || service.browseQuery.RememberedFrom == nil {
		t.Fatalf("unexpected query: %#v", service.browseQuery)
	}
	var result memory.BrowseResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Items) != 1 {
		t.Fatalf("unexpected response: %v %#v", err, result)
	}
}

func TestMetadataDiscoveryUsesAuthenticatedPrincipal(t *testing.T) {
	service := &fakeService{}
	handler := Handler(service, testProtect)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metadata", strings.NewReader(`{"facet":"topic","query":"sec","space_ids":["s1"],"limit":10}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.metadata.Principal.ID == "" || service.metadata.Facet != memory.MetadataTopic || service.metadata.Query != "sec" {
		t.Fatalf("status=%d query=%#v body=%s", response.Code, service.metadata, response.Body.String())
	}
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
	if !strings.Contains(response.Body.String(), "Browse what your agents remember") {
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

func TestBrowseRejectsTrailingJSON(t *testing.T) {
	handler := Handler(&fakeService{}, testProtect)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/browse", strings.NewReader(`{"limit":20}{"limit":1}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}
