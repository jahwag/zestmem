package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/google/uuid"
)

type Metrics struct {
	Requests          atomic.Uint64
	Limited           atomic.Uint64
	Failures          atomic.Uint64
	mu                sync.Mutex
	operationCount    map[string]uint64
	operationFailures map[string]uint64
	operationDuration map[string]time.Duration
	embeddingCount    uint64
	embeddingFailures uint64
	embeddingDuration time.Duration
}

func (m *Metrics) Observe(operation string, duration time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.operationCount == nil {
		m.operationCount = map[string]uint64{}
		m.operationFailures = map[string]uint64{}
		m.operationDuration = map[string]time.Duration{}
	}
	m.operationCount[operation]++
	m.operationDuration[operation] += duration
	if err != nil {
		m.operationFailures[operation]++
	}
}
func (m *Metrics) ObserveEmbedding(duration time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embeddingCount++
	m.embeddingDuration += duration
	if err != nil {
		m.embeddingFailures++
	}
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "agent_memory_http_requests_total %d\nagent_memory_rate_limited_total %d\nagent_memory_http_failures_total %d\n", m.Requests.Load(), m.Limited.Load(), m.Failures.Load())
		m.mu.Lock()
		defer m.mu.Unlock()
		for op, count := range m.operationCount {
			fmt.Fprintf(w, "agent_memory_operations_total{operation=%q} %d\nagent_memory_operation_failures_total{operation=%q} %d\nagent_memory_operation_duration_seconds_sum{operation=%q} %.6f\n", op, count, op, m.operationFailures[op], op, m.operationDuration[op].Seconds())
		}
		fmt.Fprintf(w, "agent_memory_embedding_requests_total %d\nagent_memory_embedding_failures_total %d\nagent_memory_embedding_duration_seconds_sum %.6f\n", m.embeddingCount, m.embeddingFailures, m.embeddingDuration.Seconds())
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func Observe(next http.Handler, metrics *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		rw := &responseWriter{ResponseWriter: w, status: 200}
		metrics.Requests.Add(1)
		next.ServeHTTP(rw, r)
		if rw.status >= 500 {
			metrics.Failures.Add(1)
		}
		slog.Info("http_request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type bucket struct {
	tokens  float64
	updated time.Time
}
type Limiter struct {
	mu          sync.Mutex
	buckets     map[string]bucket
	rate, burst float64
	metrics     *Metrics
}

func NewLimiter(perMinute, burst int, metrics *Metrics) *Limiter {
	return &Limiter{buckets: map[string]bucket{}, rate: float64(perMinute) / 60, burst: float64(burst), metrics: metrics}
}

func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", 401)
			return
		}
		now := time.Now()
		l.mu.Lock()
		b := l.buckets[p.ID]
		if b.updated.IsZero() {
			b.tokens = l.burst
			b.updated = now
		}
		b.tokens = min(l.burst, b.tokens+now.Sub(b.updated).Seconds()*l.rate)
		b.updated = now
		allowed := b.tokens >= 1
		if allowed {
			b.tokens--
		}
		l.buckets[p.ID] = b
		l.mu.Unlock()
		if !allowed {
			l.metrics.Limited.Add(1)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(1/l.rate))))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
