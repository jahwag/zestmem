package authn

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
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
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode ||
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
		flows: make(map[[sha256.Size]byte]browserOIDCFlow),
		now:   time.Now, random: rand.Reader,
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
		if cookie.Name == ConsoleSessionCookie {
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

func TestBrowserOIDCFlowCapacityEvictsOldestAndReplacesSameBrowser(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	provider := &BrowserOIDC{
		flows:  make(map[[sha256.Size]byte]browserOIDCFlow),
		now:    func() time.Time { return now },
		random: rand.Reader,
	}
	var firstState, latestState string
	for i := 0; i <= maxOIDCFlows; i++ {
		state, _, _, err := provider.newFlow("")
		if err != nil {
			t.Fatalf("flow %d: %v", i, err)
		}
		if i == 0 {
			firstState = state
		}
		latestState = state
		now = now.Add(time.Millisecond)
	}
	if len(provider.flows) != maxOIDCFlows {
		t.Fatalf("flow count = %d, want %d", len(provider.flows), maxOIDCFlows)
	}
	if _, ok := provider.consumeFlow(firstState); ok {
		t.Fatal("oldest abandoned flow survived bounded-capacity eviction")
	}
	if _, ok := provider.consumeFlow(latestState); !ok {
		t.Fatal("latest flow was unavailable after capacity eviction")
	}

	state, _, _, err := provider.newFlow("")
	if err != nil {
		t.Fatal(err)
	}
	countBefore := len(provider.flows)
	replacement, _, _, err := provider.newFlow(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.flows) != countBefore {
		t.Fatalf("same-browser replacement grew flow count from %d to %d", countBefore, len(provider.flows))
	}
	if _, ok := provider.consumeFlow(state); ok {
		t.Fatal("replaced browser flow remained usable")
	}
	if _, ok := provider.consumeFlow(replacement); !ok {
		t.Fatal("replacement browser flow was unavailable")
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
