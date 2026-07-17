package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestMetricsExposeOperationDurationHistogram(t *testing.T) {
	metrics := &Metrics{}
	metrics.Observe("recall", 100*time.Millisecond, nil)
	metrics.Observe("recall", 600*time.Millisecond, assertError{})
	w := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{
		`agent_memory_operation_duration_seconds_bucket{operation="recall",le="0.25"} 1`,
		`agent_memory_operation_duration_seconds_bucket{operation="recall",le="1"} 2`,
		`agent_memory_operation_duration_seconds_count{operation="recall"} 2`,
		`agent_memory_operation_duration_seconds_sum{operation="recall"} 0.700000`,
		`agent_memory_operation_failures_total{operation="recall"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

type assertError struct{}

func (assertError) Error() string { return "expected" }
