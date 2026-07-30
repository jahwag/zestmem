package authn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	verifierHTTPTimeout  = 5 * time.Second
	jwksRefreshCooldown  = time.Minute
	maxOIDCResponseBytes = 1 << 20
)

type cachedResponse struct {
	status     string
	statusCode int
	header     http.Header
	body       []byte
	storedAt   time.Time
}

// cooldownTransport bounds OIDC discovery/JWKS responses and reuses a recent
// successful response. The latter prevents attacker-selected unknown key IDs
// from turning every rejected JWT into an upstream JWKS request.
type cooldownTransport struct {
	base     http.RoundTripper
	cooldown time.Duration

	mu       sync.Mutex
	cache    map[string]cachedResponse
	failures uint64
}

func newOIDCClientContext(ctx context.Context) (context.Context, *cooldownTransport) {
	transport := &cooldownTransport{
		base:     http.DefaultTransport,
		cooldown: jwksRefreshCooldown,
		cache:    make(map[string]cachedResponse),
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   verifierHTTPTimeout,
	}
	return oidc.ClientContext(ctx, client), transport
}

func (t *cooldownTransport) failureCount() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failures
}

func (t *cooldownTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		return t.base.RoundTrip(request)
	}
	key := request.URL.String()
	now := time.Now()
	t.mu.Lock()
	cached, ok := t.cache[key]
	if ok && now.Sub(cached.storedAt) < t.cooldown {
		t.mu.Unlock()
		return responseFromCache(request, cached), nil
	}
	t.mu.Unlock()

	response, err := t.base.RoundTrip(request)
	if err != nil {
		t.recordFailure()
		return nil, errors.Join(ErrVerifierUnavailable, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxOIDCResponseBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.recordFailure()
		return nil, errors.Join(ErrVerifierUnavailable, readErr, closeErr)
	}
	if len(body) > maxOIDCResponseBytes {
		t.recordFailure()
		return nil, fmt.Errorf(
			"%w: OIDC response exceeds %d bytes",
			ErrVerifierUnavailable,
			maxOIDCResponseBytes,
		)
	}
	entry := cachedResponse{
		status:     response.Status,
		statusCode: response.StatusCode,
		header:     response.Header.Clone(),
		body:       body,
		storedAt:   now,
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.recordFailure()
		return nil, fmt.Errorf(
			"%w: OIDC endpoint returned %s",
			ErrVerifierUnavailable,
			response.Status,
		)
	}
	t.mu.Lock()
	t.cache[key] = entry
	t.mu.Unlock()
	return responseFromCache(request, entry), nil
}

func (t *cooldownTransport) recordFailure() {
	t.mu.Lock()
	t.failures++
	t.mu.Unlock()
}

func responseFromCache(request *http.Request, cached cachedResponse) *http.Response {
	return &http.Response{
		Status:        cached.status,
		StatusCode:    cached.statusCode,
		Header:        cached.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(cached.body)),
		ContentLength: int64(len(cached.body)),
		Request:       request,
	}
}
