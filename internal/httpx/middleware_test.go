package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/memory"
)

func TestLimiterIsScopedPerPrincipalAndReturnsRetryGuidance(t *testing.T) {
	metrics := &Metrics{}
	limiter := NewLimiter(60, 1, metrics)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	handler := limiter.Middleware(next)
	request := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r = r.WithContext(authn.WithPrincipal(r.Context(), memory.Principal{ID: id}))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if got := request("one").Code; got != 204 {
		t.Fatalf("first status=%d", got)
	}
	limited := request("one")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("limited response=%d headers=%v", limited.Code, limited.Header())
	}
	if got := request("two").Code; got != 204 {
		t.Fatalf("second principal status=%d", got)
	}
	if metrics.Limited.Load() != 1 {
		t.Fatalf("limited metric=%d", metrics.Limited.Load())
	}
}
