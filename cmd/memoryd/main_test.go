package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/httpx"
	"github.com/jahwag/zestmem/internal/memory"
)

func TestCanonicalSpaceIDNormalizesValidAliases(t *testing.T) {
	got, err := canonicalSpaceID("49400172-EC60-4646-92C8-331D1DB78A4A")
	if err != nil {
		t.Fatal(err)
	}
	if got != "49400172-ec60-4646-92c8-331d1db78a4a" {
		t.Fatalf("canonical Space ID=%q", got)
	}
	if _, err := canonicalSpaceID("not-a-space"); err == nil {
		t.Fatal("invalid Space ID was accepted")
	}
}

func TestConsoleIdentityProvidersCannotReuseMCPAuthority(t *testing.T) {
	const (
		resourceIssuer   = "https://identity.example.test/tenant/v2.0"
		resourceAudience = "zestmem-api"
	)
	for _, test := range []struct {
		name              string
		assertionIssuer   string
		assertionAudience string
		browserIssuer     string
		browserClientID   string
	}{
		{
			name:              "trusted edge assertion",
			assertionIssuer:   resourceIssuer,
			assertionAudience: resourceAudience,
		},
		{
			name:            "browser OIDC",
			browserIssuer:   resourceIssuer,
			browserClientID: resourceAudience,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateConsoleAuthoritySeparation(
				resourceIssuer,
				resourceAudience,
				test.assertionIssuer,
				test.assertionAudience,
				test.browserIssuer,
				test.browserClientID,
			); err == nil {
				t.Fatal("MCP authority reused for browser authentication")
			}
		})
	}
	if err := validateConsoleAuthoritySeparation(
		resourceIssuer,
		resourceAudience,
		resourceIssuer,
		"edge-audience",
		resourceIssuer,
		"browser-client",
	); err != nil {
		t.Fatalf("distinct console audiences rejected: %v", err)
	}
}

func TestRegisterMCPRoutesPublishesRFC9728MetadataAndChallenge(t *testing.T) {
	const (
		issuer      = "https://login.example.test/tenant/v2.0"
		resource    = "https://memory.example.test/mcp"
		metadataURL = "https://memory.example.test/.well-known/oauth-protected-resource/mcp"
	)
	verifier := func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
		return &auth.TokenInfo{
			Scopes:     []string{"Memory.Use"},
			Expiration: time.Now().Add(time.Hour),
			UserID:     "agent-1",
			Extra: map[string]any{
				"principal": memory.Principal{ID: "agent-1", Type: "agent"},
			},
		}, nil
	}
	mux := http.NewServeMux()
	limiter := httpx.NewLimiter(120, 20, &httpx.Metrics{})
	registerMCPRoutes(
		mux,
		limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})),
		verifier,
		issuer,
		resource,
		metadataURL,
		[]string{"Memory.Use"},
	)

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, resource, nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d, want 401", missing.Code)
	}
	if challenge := missing.Header().Get("WWW-Authenticate"); !strings.Contains(
		challenge,
		`resource_metadata="`+metadataURL+`"`,
	) {
		t.Fatalf("challenge omitted RFC 9728 metadata URL: %q", challenge)
	}

	metadataResponse := httptest.NewRecorder()
	mux.ServeHTTP(metadataResponse, httptest.NewRequest(http.MethodGet, metadataURL, nil))
	if metadataResponse.Code != http.StatusOK {
		t.Fatalf("metadata status=%d, want 200", metadataResponse.Code)
	}
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(metadataResponse.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Resource != resource ||
		len(metadata.AuthorizationServers) != 1 ||
		metadata.AuthorizationServers[0] != issuer ||
		len(metadata.ScopesSupported) != 1 ||
		metadata.ScopesSupported[0] != "Memory.Use" {
		t.Fatalf("metadata=%+v", metadata)
	}

	authorizedRequest := httptest.NewRequest(http.MethodPost, resource, nil)
	authorizedRequest.Header.Set("Authorization", "Bearer valid-token")
	authorized := httptest.NewRecorder()
	mux.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status=%d, want 204", authorized.Code)
	}
}

func TestRegisterConsoleAuthRoutesAllowsUnauthenticatedOIDCStart(t *testing.T) {
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
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
	oidcLogin, err := authn.NewBrowserOIDC(
		context.Background(),
		provider.URL,
		"memory-browser",
		"",
		"https://memory.example.test/console/auth/oidc/callback",
		nil,
		"sub",
		"",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := authn.NewBrowserSessions(nil, nil, "", "https://memory.example.test")
	if err != nil {
		t.Fatal(err)
	}
	sessions.WithOIDC(oidcLogin)
	mux := http.NewServeMux()
	registerConsoleAuthRoutes(mux, sessions)

	request := httptest.NewRequest(http.MethodGet, "https://memory.example.test/console/auth/oidc/start", nil)
	request.Host = "memory.example.test"
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("OIDC start status=%d, want 302", response.Code)
	}
}

func TestNewConsoleBrowserOIDCRequiresExactPublicOrigin(t *testing.T) {
	for _, key := range []string{
		"CONSOLE_OIDC_ISSUER",
		"CONSOLE_OIDC_CLIENT_ID",
		"CONSOLE_OIDC_CLIENT_SECRET",
		"CONSOLE_OIDC_CLIENT_SECRET_FILE",
		"CONSOLE_OIDC_REDIRECT_URL",
		"CONSOLE_OIDC_SCOPES",
		"CONSOLE_OIDC_SUBJECT_CLAIM",
		"CONSOLE_OIDC_REQUIRED_ROLE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CONSOLE_OIDC_ISSUER", "https://identity.example.test/tenant/v2.0")
	t.Setenv("CONSOLE_OIDC_CLIENT_ID", "memory-browser")
	t.Setenv("CONSOLE_OIDC_REDIRECT_URL", "https://memory.example.test/console/auth/oidc/callback")
	if _, err := newConsoleBrowserOIDC(context.Background(), "", nil); err == nil {
		t.Fatal("OIDC configuration without public origin succeeded")
	}
	if _, err := newConsoleBrowserOIDC(context.Background(), "https://other.example.test", nil); err == nil {
		t.Fatal("OIDC configuration with mismatched public origin succeeded")
	}
}

func TestNewConsoleBrowserOIDCLoadsExclusiveSecretFile(t *testing.T) {
	for _, key := range []string{
		"CONSOLE_OIDC_CLIENT_SECRET",
		"CONSOLE_OIDC_CLIENT_SECRET_FILE",
		"CONSOLE_OIDC_REDIRECT_URL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CONSOLE_OIDC_ISSUER", "https://identity.example.test/tenant/v2.0")
	t.Setenv("CONSOLE_OIDC_CLIENT_ID", "memory-browser")
	secretPath := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secretPath, []byte("test-client-secret\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONSOLE_OIDC_CLIENT_SECRET_FILE", secretPath)
	if _, err := newConsoleBrowserOIDC(context.Background(), "https://memory.example.test", nil); err != nil {
		t.Fatalf("secret file rejected: %v", err)
	}
	t.Setenv("CONSOLE_OIDC_CLIENT_SECRET", "ambiguous-inline-secret")
	if _, err := newConsoleBrowserOIDC(context.Background(), "https://memory.example.test", nil); err == nil {
		t.Fatal("simultaneous secret value and file succeeded")
	}
}

func TestConsoleAPIProtectionPreservesBearerClients(t *testing.T) {
	lookup := staticPrincipalLookup{principal: memory.Principal{ID: "reader-1", Type: "human"}}
	authenticator := authn.NewAPIKeyOnly(lookup)
	sessions, err := authn.NewBrowserSessions(authenticator, nil, "", "https://memory.example.test")
	if err != nil {
		t.Fatal(err)
	}
	limiter := httpx.NewLimiter(120, 20, &httpx.Metrics{})
	handler := protectConsoleAPI(
		sessions,
		authenticator,
		limiter,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := authn.PrincipalFromContext(r.Context())
			if !ok || principal.ID != "reader-1" {
				t.Fatalf("bearer principal = %+v, ok=%v", principal, ok)
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	request := httptest.NewRequest(http.MethodGet, "https://memory.example.test/api/v1/catalog", nil)
	request.Host = "memory.example.test"
	request.Header.Set("Authorization", "Bearer am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("bearer console API status=%d, want 204", response.Code)
	}
}

func TestConsoleAPIAuthorizationHeaderNeverFallsBackToBrowserSession(t *testing.T) {
	lookup := staticPrincipalLookup{principal: memory.Principal{ID: "reader-1", Type: "human"}}
	authenticator := authn.NewAPIKeyOnly(lookup)
	sessions, err := authn.NewBrowserSessions(authenticator, nil, "", "https://memory.example.test")
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(
		http.MethodPost,
		"https://memory.example.test/console/auth/token",
		strings.NewReader(`{"token":"am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`),
	)
	login.Host = "memory.example.test"
	login.Header.Set("Origin", "https://memory.example.test")
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	sessions.LoginHandler(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("browser login status = %d, cookies = %+v", loginResponse.Code, loginResponse.Result().Cookies())
	}

	reached := false
	handler := protectConsoleAPI(
		sessions,
		authenticator,
		httpx.NewLimiter(120, 20, &httpx.Metrics{}),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	request := httptest.NewRequest(http.MethodGet, "https://memory.example.test/api/v1/catalog", nil)
	request.Host = "memory.example.test"
	request.Header.Set("Authorization", "Basic attacker-controlled")
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || reached {
		t.Fatalf("malformed authorization status = %d, reached = %v", response.Code, reached)
	}
}

func TestConsoleAuthRoutesBoundInvalidKeyLookupsBeforeAuthentication(t *testing.T) {
	apiKeyCalls := 0
	lookup := staticPrincipalLookup{apiKeyCalls: &apiKeyCalls, apiKeyErr: memory.ErrNotFound}
	sessions, err := authn.NewBrowserSessions(
		authn.NewAPIKeyOnly(lookup),
		nil,
		"",
		"https://memory.example.test",
	)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	sessions.WithLoginAdmission(httpx.NewLimiter(60, 1, &httpx.Metrics{}).AdmitUnauthenticated)
	registerConsoleAuthRoutes(mux, sessions)
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(
			http.MethodPost,
			"https://memory.example.test/console/auth/token",
			strings.NewReader(`{"token":"am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`),
		)
		r.Host = "memory.example.test"
		r.Header.Set("Origin", "https://memory.example.test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	cheapRejection := httptest.NewRequest(
		http.MethodPost,
		"https://memory.example.test/console/auth/token",
		strings.NewReader(`{"token":"am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`),
	)
	cheapRejection.Host = "memory.example.test"
	cheapRejection.Header.Set("Origin", "https://memory.example.test")
	cheapResponse := httptest.NewRecorder()
	mux.ServeHTTP(cheapResponse, cheapRejection)
	if cheapResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("cheap rejection status = %d", cheapResponse.Code)
	}

	if got := request().Code; got != http.StatusUnauthorized {
		t.Fatalf("first invalid login status = %d", got)
	}
	if got := request().Code; got != http.StatusTooManyRequests {
		t.Fatalf("limited invalid login status = %d", got)
	}
	if apiKeyCalls != 1 {
		t.Fatalf("API-key lookups = %d, want 1", apiKeyCalls)
	}
}

type staticPrincipalLookup struct {
	principal   memory.Principal
	apiKeyErr   error
	apiKeyCalls *int
}

func (l staticPrincipalLookup) ByExternalIdentity(context.Context, string, string) (memory.Principal, error) {
	return l.principal, nil
}

func (l staticPrincipalLookup) ByAPIKeyDigest(context.Context, []byte) (memory.Principal, error) {
	if l.apiKeyCalls != nil {
		*l.apiKeyCalls++
	}
	return l.principal, l.apiKeyErr
}
