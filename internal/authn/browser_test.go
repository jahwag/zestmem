package authn

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jahwag/zestmem/internal/memory"
	"golang.org/x/oauth2"
)

const browserTestKey = "am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestBrowserSessionsRejectUnsafeAssertionOrigins(t *testing.T) {
	assertion := &Authenticator{}
	for _, origin := range []string{"", "http://memory.example.com"} {
		if _, err := NewBrowserSessions(nil, assertion, "X-Edge-Assertion", origin); err == nil {
			t.Fatalf("assertion origin %q unexpectedly accepted", origin)
		}
	}
	for _, origin := range []string{"https://memory.example.com", "http://127.0.0.1:8080"} {
		if _, err := NewBrowserSessions(nil, assertion, "X-Edge-Assertion", origin); err != nil {
			t.Fatalf("safe assertion origin %q rejected: %v", origin, err)
		}
	}
}

func TestBrowserSessionsExchangeNativeKeyWithoutBrowserBearerStorage(t *testing.T) {
	lookup := &fakeLookup{principal: memory.Principal{
		ID: "reader-1", Type: "human", DisplayName: "Reader One",
	}}
	sessions, err := NewBrowserSessions(
		NewAPIKeyOnly(lookup),
		nil,
		"",
		"https://memory.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	sessions.now = func() time.Time { return now }

	login := httptest.NewRequest(
		http.MethodPost,
		"https://memory.example.com/console/auth/token",
		strings.NewReader(`{"token":"`+browserTestKey+`"}`),
	)
	login.Host = "memory.example.com"
	login.Header.Set("Origin", "https://memory.example.com")
	login.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	sessions.LoginHandler(recorder, login)
	response := recorder.Result()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("login = %d cookies=%+v", response.StatusCode, response.Cookies())
	}
	cookie := response.Cookies()[0]
	if !strings.HasPrefix(cookie.Name, "__Host-") || cookie.Path != "/" ||
		!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode ||
		strings.Contains(cookie.Value, browserTestKey) {
		t.Fatalf("unsafe session cookie = %+v", cookie)
	}

	called := false
	protected := sessions.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.ID != "reader-1" {
			t.Errorf("session principal = %+v, %v", principal, ok)
		}
	}))
	request := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	request.Host = "memory.example.com"
	request.AddCookie(cookie)
	protected.ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("fresh server-side browser session was rejected")
	}

	now = now.Add(consoleSessionTTL + time.Second)
	expired := httptest.NewRecorder()
	protected.ServeHTTP(expired, request)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d", expired.Code)
	}

	wrongHost := httptest.NewRequest(
		http.MethodPost,
		"https://attacker.example/console/auth/token",
		strings.NewReader(`{"token":"`+browserTestKey+`"}`),
	)
	wrongHost.Host = "attacker.example"
	wrongHost.Header.Set("Origin", "https://memory.example.com")
	wrongHost.Header.Set("Content-Type", "application/json")
	notFound := httptest.NewRecorder()
	sessions.LoginHandler(notFound, wrongHost)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("wrong-host login status = %d", notFound.Code)
	}
}

func TestBrowserRepeatedLoginReplacesExistingSession(t *testing.T) {
	lookup := &fakeLookup{principal: memory.Principal{ID: "reader-1", Type: "human"}}
	sessions, err := NewBrowserSessions(NewAPIKeyOnly(lookup), nil, "", "https://memory.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for i := 0; i <= maxSessions; i++ {
		request := httptest.NewRequest(
			http.MethodPost,
			"https://memory.example.com/console/auth/token",
			strings.NewReader(`{"token":"`+browserTestKey+`"}`),
		)
		request.Host = "memory.example.com"
		request.Header.Set("Origin", "https://memory.example.com")
		request.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		sessions.LoginHandler(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("relogin %d status = %d, want 200", i, response.Code)
		}
		cookie = response.Result().Cookies()[0]
	}
	if len(sessions.sessions) != 1 {
		t.Fatalf("active sessions = %d, want replacement to leave one", len(sessions.sessions))
	}
}

func TestBrowserPrincipalCannotExhaustGlobalSessionCapacity(t *testing.T) {
	lookup := &fakeLookup{principal: memory.Principal{ID: "reader-1", Type: "human"}}
	sessions, err := NewBrowserSessions(NewAPIKeyOnly(lookup), nil, "", "https://memory.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxSessions; i++ {
		request := httptest.NewRequest(
			http.MethodPost,
			"https://memory.example.com/console/auth/token",
			strings.NewReader(`{"token":"`+browserTestKey+`"}`),
		)
		request.Host = "memory.example.com"
		request.Header.Set("Origin", "https://memory.example.com")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		sessions.LoginHandler(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("login %d status = %d, want 200", i, response.Code)
		}
	}
	if len(sessions.sessions) != maxSessionsPerPrincipal {
		t.Fatalf("principal sessions = %d, want %d", len(sessions.sessions), maxSessionsPerPrincipal)
	}
}

func TestNativeBrowserSessionCapsExpiryAndRevalidatesRevocation(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	keyExpiry := now.Add(30 * time.Minute)
	lookup := &fakeLookup{
		principal: memory.Principal{ID: "reader-1", Type: "human"},
		expiry:    keyExpiry,
	}
	sessions, err := NewBrowserSessions(
		NewAPIKeyOnly(lookup),
		nil,
		"",
		"https://memory.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	sessions.now = func() time.Time { return now }
	login := httptest.NewRequest(
		http.MethodPost,
		"https://memory.example.com/console/auth/token",
		strings.NewReader(`{"token":"`+browserTestKey+`"}`),
	)
	login.Host = "memory.example.com"
	login.Header.Set("Origin", "https://memory.example.com")
	login.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	sessions.LoginHandler(recorder, login)
	response := recorder.Result()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("login = %d cookies=%+v", response.StatusCode, response.Cookies())
	}
	cookie := response.Cookies()[0]
	if !cookie.Expires.Equal(keyExpiry) {
		t.Fatalf("session expiry = %s, want key expiry %s", cookie.Expires, keyExpiry)
	}
	protected := sessions.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	request.Host = "memory.example.com"
	request.AddCookie(cookie)
	fresh := httptest.NewRecorder()
	protected.ServeHTTP(fresh, request)
	if fresh.Code != http.StatusNoContent {
		t.Fatalf("fresh session status = %d", fresh.Code)
	}

	lookup.apiKeyErr = errors.New("database temporarily unavailable")
	unavailable := httptest.NewRecorder()
	protected.ServeHTTP(unavailable, request)
	if unavailable.Code != http.StatusServiceUnavailable || len(unavailable.Result().Cookies()) != 0 {
		t.Fatalf("unavailable native lookup status = %d, cookies = %+v", unavailable.Code, unavailable.Result().Cookies())
	}
	lookup.apiKeyErr = nil
	recovered := httptest.NewRecorder()
	protected.ServeHTTP(recovered, request)
	if recovered.Code != http.StatusNoContent {
		t.Fatalf("recovered native lookup status = %d", recovered.Code)
	}

	lookup.apiKeyErr = memory.ErrNotFound
	revoked := httptest.NewRecorder()
	protected.ServeHTTP(revoked, request)
	if revoked.Code != http.StatusUnauthorized ||
		len(revoked.Result().Cookies()) != 1 ||
		revoked.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf(
			"revoked session status/cookies = %d %+v",
			revoked.Code,
			revoked.Result().Cookies(),
		)
	}
}

func TestBrowserAssertionUsesOIDRoleAndCredentialExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://edge.example"
	lookup := &fakeLookup{principal: memory.Principal{ID: "edge-reader", Type: "human"}}
	assertion := &Authenticator{
		issuer: issuer, subjectClaim: "oid", requiredRole: "Memory.Reader",
		verifier: oidc.NewVerifier(
			issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}},
			&oidc.Config{ClientID: "memory-ui"},
		),
		lookup: lookup,
	}
	sessions, err := NewBrowserSessions(
		NewAPIKeyOnly(lookup),
		assertion,
		"X-Edge-Assertion",
		"https://memory.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(5 * time.Minute).Truncate(time.Second)
	raw := signBrowserToken(t, key, issuer, "memory-ui", expires, map[string]any{
		"oid": "object-123",
		"roles": []string{
			"Memory.Reader",
		},
	})
	request := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	request.Host = "memory.example.com"
	request.Header.Set("X-Edge-Assertion", raw)
	recorder := httptest.NewRecorder()
	called := false
	sessions.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})).ServeHTTP(recorder, request)
	if !called || len(recorder.Result().Cookies()) != 1 ||
		lookup.issuer != issuer || lookup.subject != "object-123" {
		t.Fatalf("assertion exchange called=%v cookies=%+v lookup=%q/%q",
			called, recorder.Result().Cookies(), lookup.issuer, lookup.subject)
	}
	sessions.mu.Lock()
	for _, session := range sessions.sessions {
		if session.expiresAt.After(expires) {
			t.Errorf("session expiry %s exceeds assertion expiry %s", session.expiresAt, expires)
		}
	}
	sessions.mu.Unlock()
	cookie := recorder.Result().Cookies()[0]
	lookup.externalErr = errors.New("database temporarily unavailable")
	unavailableRequest := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	unavailableRequest.Host = "memory.example.com"
	unavailableRequest.AddCookie(cookie)
	unavailable := httptest.NewRecorder()
	sessions.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unavailable external lookup reached protected handler")
	})).ServeHTTP(unavailable, unavailableRequest)
	if unavailable.Code != http.StatusServiceUnavailable || len(unavailable.Result().Cookies()) != 0 {
		t.Fatalf("unavailable external lookup status = %d, cookies = %+v", unavailable.Code, unavailable.Result().Cookies())
	}
	lookup.externalErr = nil
	recoveredRequest := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	recoveredRequest.Host = "memory.example.com"
	recoveredRequest.AddCookie(cookie)
	recovered := httptest.NewRecorder()
	sessions.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recovered, recoveredRequest)
	if recovered.Code != http.StatusNoContent {
		t.Fatalf("recovered external lookup status = %d", recovered.Code)
	}

	lookup.externalErr = memory.ErrNotFound
	revokedRequest := httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	revokedRequest.Host = "memory.example.com"
	revokedRequest.AddCookie(cookie)
	revoked := httptest.NewRecorder()
	sessions.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("revoked external session reached protected handler")
	})).ServeHTTP(revoked, revokedRequest)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked external session status = %d, want 401", revoked.Code)
	}
	lookup.externalErr = nil

	rejected := signBrowserToken(t, key, issuer, "memory-ui", expires, map[string]any{
		"oid": "object-123", "roles": []string{"Other.Role"},
	})
	request = httptest.NewRequest(http.MethodGet, "https://memory.example.com/api/v1/catalog", nil)
	request.Host = "memory.example.com"
	request.Header.Set("X-Edge-Assertion", rejected)
	recorder = httptest.NewRecorder()
	sessions.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("wrong-role assertion reached protected handler")
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-role assertion status = %d", recorder.Code)
	}
}

func TestBrowserOIDCPreservesExactIssuerIdentifier(t *testing.T) {
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		issuer := provider.URL + "/"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                provider.URL + "/authorize",
			"token_endpoint":                        provider.URL + "/token",
			"jwks_uri":                              provider.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer provider.Close()
	issuer := provider.URL + "/"
	oidcLogin, err := NewBrowserOIDC(
		context.Background(),
		issuer,
		"memory-browser",
		"",
		"https://memory.example.com/console/auth/oidc/callback",
		nil,
		"sub",
		"",
		&fakeLookup{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if oidcLogin.authenticator.issuer != issuer {
		t.Fatalf("issuer = %q, want exact %q", oidcLogin.authenticator.issuer, issuer)
	}
	if !contains(oidcLogin.config.Scopes, oidc.ScopeOpenID) {
		t.Fatalf("default browser OIDC scopes = %v, want openid", oidcLogin.config.Scopes)
	}
}

func TestBrowserOIDCRejectsPlaintextNonLoopbackEndpoints(t *testing.T) {
	tests := []struct {
		issuer   string
		redirect string
	}{
		{"http://identity.example.test", "https://memory.example.test/console/auth/oidc/callback"},
		{"https://identity.example.test", "http://memory.example.test/console/auth/oidc/callback"},
	}
	for _, test := range tests {
		if _, err := NewBrowserOIDC(
			context.Background(), test.issuer, "memory-browser", "", test.redirect,
			nil, "sub", "", &fakeLookup{},
		); err == nil {
			t.Fatalf("plaintext public OIDC endpoints accepted: issuer=%q redirect=%q", test.issuer, test.redirect)
		}
	}
}

func TestBrowserOIDCDiscoveryOutageDoesNotBlockConstructionAndRecovers(t *testing.T) {
	unavailable := true
	requests := 0
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		requests++
		if unavailable {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                provider.URL,
			"authorization_endpoint":                provider.URL + "/authorize",
			"token_endpoint":                        provider.URL + "/token",
			"jwks_uri":                              provider.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer provider.Close()
	oidcLogin, err := NewBrowserOIDC(
		context.Background(), provider.URL, "memory-browser", "",
		"https://memory.example.com/console/auth/oidc/callback", nil, "sub", "", &fakeLookup{},
	)
	if err != nil {
		t.Fatalf("provider outage blocked construction: %v", err)
	}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	oidcLogin.now = func() time.Time { return now }
	sessions, err := NewBrowserSessions(nil, nil, "", "https://memory.example.com")
	if err != nil {
		t.Fatal(err)
	}
	sessions.WithOIDC(oidcLogin)
	start := func() int {
		request := httptest.NewRequest(http.MethodGet, "https://memory.example.com/console/auth/oidc/start", nil)
		request.Host = "memory.example.com"
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		sessions.OIDCStartHandler(response, request)
		return response.Code
	}
	if got := start(); got != http.StatusServiceUnavailable || requests != 1 {
		t.Fatalf("outage start status/requests = %d/%d, want 503/1", got, requests)
	}
	unavailable = false
	now = now.Add(browserOIDCDiscoveryRetry + time.Second)
	if got := start(); got != http.StatusFound || requests != 2 {
		t.Fatalf("recovered start status/requests = %d/%d, want 302/2", got, requests)
	}
}

func TestBrowserOIDCUsesStateNonceAndPKCE(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const (
		issuer   = "https://id.example"
		clientID = "memory-browser"
	)
	lookup := &fakeLookup{principal: memory.Principal{ID: "oidc-reader", Type: "human"}}
	authenticator := &Authenticator{
		issuer: issuer, subjectClaim: "sub",
		verifier: oidc.NewVerifier(
			issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}},
			&oidc.Config{ClientID: clientID},
		),
		lookup: lookup,
	}
	var expectedNonce string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("code") != "accepted-code" || r.Form.Get("code_verifier") == "" {
			t.Errorf("token exchange form = %v", r.Form)
		}
		raw := signBrowserToken(
			t,
			key,
			issuer,
			clientID,
			time.Now().Add(time.Hour),
			map[string]any{"nonce": expectedNonce},
		)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "opaque-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     raw,
		})
	}))
	defer tokenServer.Close()

	browserOIDC := &BrowserOIDC{
		authenticator: authenticator,
		config: oauth2.Config{
			ClientID: clientID,
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://id.example/authorize",
				TokenURL: tokenServer.URL,
			},
			RedirectURL: "https://memory.example.com/console/auth/oidc/callback",
			Scopes:      []string{"openid"},
		},
		usedFlows: make(map[[sha256.Size]byte]time.Time),
		now:       time.Now, random: rand.Reader,
	}
	sessions, err := NewBrowserSessions(
		NewAPIKeyOnly(lookup),
		nil,
		"",
		"https://memory.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	sessions.WithOIDC(browserOIDC)

	start := httptest.NewRequest(
		http.MethodGet,
		"https://memory.example.com/console/auth/oidc/start",
		nil,
	)
	start.Host = "memory.example.com"
	start.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	sessions.OIDCStartHandler(recorder, start)
	response := recorder.Result()
	if response.StatusCode != http.StatusFound || len(response.Cookies()) != 1 {
		t.Fatalf("OIDC start = %d cookies=%+v", response.StatusCode, response.Cookies())
	}
	if flowCookie := response.Cookies()[0]; !strings.HasPrefix(flowCookie.Name, "__Host-") || flowCookie.Path != "/" ||
		!flowCookie.Secure || !flowCookie.HttpOnly || flowCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe OIDC flow cookie = %+v", flowCookie)
	}
	authorizationURL, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")
	expectedNonce = authorizationURL.Query().Get("nonce")
	if state == "" || expectedNonce == "" ||
		authorizationURL.Query().Get("code_challenge") == "" ||
		authorizationURL.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL = %s", authorizationURL)
	}
	for i := 0; i < 256; i++ {
		additionalStart := httptest.NewRequest(
			http.MethodGet,
			"https://memory.example.com/console/auth/oidc/start",
			nil,
		)
		additionalStart.Host = "memory.example.com"
		additionalStart.Header.Set("Sec-Fetch-Site", "same-origin")
		additionalResponse := httptest.NewRecorder()
		sessions.OIDCStartHandler(additionalResponse, additionalStart)
		if additionalResponse.Code != http.StatusFound {
			t.Fatalf("additional OIDC start %d status = %d, want 302", i, additionalResponse.Code)
		}
	}

	callback := httptest.NewRequest(
		http.MethodGet,
		"https://memory.example.com/console/auth/oidc/callback?code=accepted-code&state="+url.QueryEscape(state),
		nil,
	)
	callback.Host = "memory.example.com"
	callback.AddCookie(response.Cookies()[0])
	recorder = httptest.NewRecorder()
	sessions.OIDCCallbackHandler(recorder, callback)
	result := recorder.Result()
	if result.StatusCode != http.StatusSeeOther || result.Header.Get("Location") != "/console/" {
		body, _ := io.ReadAll(result.Body)
		t.Fatalf("OIDC callback = %d location=%q body=%s",
			result.StatusCode, result.Header.Get("Location"), body)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range result.Cookies() {
		if cookie.Name == sessions.sessionCookieName() {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("OIDC session cookie = %+v", result.Cookies())
	}

	replay := httptest.NewRecorder()
	sessions.OIDCCallbackHandler(replay, callback)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed callback status = %d", replay.Code)
	}
}

func TestBrowserOIDCExchangeLimitDoesNotConsumeRetryableState(t *testing.T) {
	tokenRequests := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests++
		http.Error(w, "provider rejected code", http.StatusUnauthorized)
	}))
	defer tokenServer.Close()

	const (
		issuer   = "https://id.example"
		clientID = "memory-browser"
	)
	lookup := &fakeLookup{principal: memory.Principal{ID: "oidc-reader", Type: "human"}}
	browserOIDC := &BrowserOIDC{
		authenticator: &Authenticator{
			issuer:       issuer,
			audience:     clientID,
			subjectClaim: "sub",
			verifier: oidc.NewVerifier(
				issuer,
				&oidc.StaticKeySet{},
				&oidc.Config{ClientID: clientID},
			),
			lookup: lookup,
		},
		issuer:   issuer,
		clientID: clientID,
		config: oauth2.Config{
			ClientID: clientID,
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://id.example/authorize",
				TokenURL: tokenServer.URL,
			},
			RedirectURL: "https://memory.example.com/console/auth/oidc/callback",
			Scopes:      []string{"openid"},
		},
		usedFlows:     make(map[[sha256.Size]byte]time.Time),
		exchangeSlots: make(chan struct{}, 1),
		now:           time.Now,
		random:        rand.Reader,
	}
	sessions, err := NewBrowserSessions(
		NewAPIKeyOnly(lookup),
		nil,
		"",
		"https://memory.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	sessions.WithOIDC(browserOIDC)

	start := httptest.NewRequest(
		http.MethodGet,
		"https://memory.example.com/console/auth/oidc/start",
		nil,
	)
	start.Host = "memory.example.com"
	start.Header.Set("Sec-Fetch-Site", "same-origin")
	startResponse := httptest.NewRecorder()
	sessions.OIDCStartHandler(startResponse, start)
	if startResponse.Code != http.StatusFound || len(startResponse.Result().Cookies()) != 1 {
		t.Fatalf("OIDC start status = %d, cookies = %+v", startResponse.Code, startResponse.Result().Cookies())
	}
	flowCookie := startResponse.Result().Cookies()[0]

	browserOIDC.exchangeSlots <- struct{}{}
	callbackURL := "https://memory.example.com/console/auth/oidc/callback?state=" +
		url.QueryEscape(flowCookie.Value) + "&code=test-code"
	callback := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	callback.Host = "memory.example.com"
	callback.AddCookie(flowCookie)
	limited := httptest.NewRecorder()
	sessions.OIDCCallbackHandler(limited, callback)
	if limited.Code != http.StatusTooManyRequests || tokenRequests != 0 {
		t.Fatalf("limited callback status = %d, token requests = %d", limited.Code, tokenRequests)
	}

	<-browserOIDC.exchangeSlots
	retry := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	retry.Host = "memory.example.com"
	retry.AddCookie(flowCookie)
	retried := httptest.NewRecorder()
	sessions.OIDCCallbackHandler(retried, retry)
	if retried.Code != http.StatusUnauthorized || tokenRequests == 0 {
		t.Fatalf("retried callback status = %d, token requests = %d", retried.Code, tokenRequests)
	}
}

func TestBrowserOIDCReplayMarkersStayBoundedWithoutRejectingNewFlow(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	provider := &BrowserOIDC{
		now:    func() time.Time { return now },
		random: rand.Reader,
	}
	var latestState string
	for i := 0; i <= maxOIDCReplayMarkers; i++ {
		state, _, _, err := provider.newFlow("")
		if err != nil {
			t.Fatalf("flow %d: %v", i, err)
		}
		if _, ok := provider.consumeFlow(state); !ok {
			t.Fatalf("flow %d was rejected", i)
		}
		latestState = state
		now = now.Add(time.Millisecond)
	}
	if len(provider.usedFlows) != maxOIDCReplayMarkers {
		t.Fatalf("replay markers = %d, want %d", len(provider.usedFlows), maxOIDCReplayMarkers)
	}
	if _, ok := provider.consumeFlow(latestState); ok {
		t.Fatal("latest consumed state replayed")
	}
}

func signBrowserToken(
	t *testing.T,
	key *rsa.PrivateKey,
	issuer string,
	audience string,
	expiry time.Time,
	extra map[string]any,
) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{
			Algorithm: jose.RS256,
			Key:       jose.JSONWebKey{Key: key, KeyID: "browser-test"},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	builder := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: "subject-123", Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(expiry),
	})
	if extra != nil {
		builder = builder.Claims(extra)
	}
	raw, err := builder.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
