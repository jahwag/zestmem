package authn

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jahwag/zestmem/internal/memory"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestAPIKeyAuthenticationUsesDigest(t *testing.T) {
	lookup := &fakeLookup{principal: memory.Principal{ID: "agent", Type: "agent"}}
	auth := NewAPIKeyOnly(lookup)
	secret := APIKeyPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	got, err := auth.Authenticate(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(secret))
	if string(lookup.digest) != string(want[:]) || got.ID != "agent" {
		t.Fatalf("digest=%x principal=%#v", lookup.digest, got)
	}
}

func TestGeneratedAPIKeysHaveOneAcceptedDotFreeFormat(t *testing.T) {
	for range 100 {
		secret, err := GenerateAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(secret, ".") || !isAPIKey(secret) {
			t.Fatalf("invalid generated key format %q", secret)
		}
	}
	for _, malformed := range []string{"secret", "am_short", "am_header.payload.signature"} {
		if isAPIKey(malformed) {
			t.Fatalf("accepted malformed API key %q", malformed)
		}
	}
}

func TestAPIKeyOnlyRejectsCredentialsOutsideIssuedFormat(t *testing.T) {
	lookup := &fakeLookup{}
	auth := NewAPIKeyOnly(lookup)
	if _, err := auth.Authenticate(context.Background(), "header.payload.signature"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error=%v, want ErrUnauthenticated", err)
	}
	if lookup.digest != nil {
		t.Fatal("malformed credential reached API key lookup")
	}
}

func TestOIDCValidatesIssuerAudienceAndExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"id_token"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	lookup := &fakeLookup{principal: memory.Principal{ID: "agent", Type: "agent"}}
	auth, err := New(context.Background(), issuer, "memory-api", lookup)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(audience string, expiry time.Time, extra map[string]any) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "test"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		builder := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "agent-subject", Audience: jwt.Audience{audience}, IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(expiry)})
		if extra != nil {
			builder = builder.Claims(extra)
		}
		raw, err := builder.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if p, err := auth.Authenticate(context.Background(), sign("memory-api", time.Now().Add(time.Hour), nil)); err != nil || p.ID != "agent" {
		t.Fatalf("valid token principal=%#v error=%v", p, err)
	}
	if _, err := auth.Authenticate(context.Background(), sign("other-api", time.Now().Add(time.Hour), nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong audience error=%v", err)
	}
	if _, err := auth.Authenticate(context.Background(), sign("memory-api", time.Now().Add(-time.Hour), nil)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token error=%v", err)
	}
	if p, err := auth.Authenticate(context.Background(), sign("memory-api", time.Now().Add(time.Hour), map[string]any{
		"roles": "provider-specific-role-shape",
		"scp":   []string{"provider-specific-scope-shape"},
	})); err != nil || p.ID != "agent" {
		t.Fatalf("irrelevant custom claims principal=%#v error=%v", p, err)
	}
	info, err := auth.TokenVerifier(context.Background(), sign("memory-api", time.Now().Add(time.Hour), map[string]any{
		"scope": "standard.read standard.write",
		"scp":   "entra.read",
		"roles": []string{"ZestMem.Agent"},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(info.Scopes, " "), "standard.read standard.write entra.read"; got != want {
		t.Fatalf("token scopes = %q, want %q", got, want)
	}
}

func TestOIDCAuthorizedPartyPolicyDistinguishesIDAndAccessTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const (
		issuer   = "https://identity.example"
		audience = "memory-api"
	)
	lookup := &fakeLookup{principal: memory.Principal{ID: "agent", Type: "agent"}}
	authenticator := &Authenticator{
		issuer:                  issuer,
		audience:                audience,
		subjectClaim:            "sub",
		validateAuthorizedParty: true,
		verifier: oidc.NewVerifier(
			issuer,
			&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}},
			&oidc.Config{ClientID: audience},
		),
		lookup: lookup,
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "test"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(audiences jwt.Audience, authorizedParty string) string {
		t.Helper()
		builder := jwt.Signed(signer).Claims(jwt.Claims{
			Issuer:   issuer,
			Subject:  "agent-subject",
			Audience: audiences,
			IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		})
		if authorizedParty != "" {
			builder = builder.Claims(map[string]any{"azp": authorizedParty})
		}
		raw, err := builder.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	multipleAudiences := jwt.Audience{audience, "another-api"}
	if _, err := authenticator.Authenticate(context.Background(), sign(multipleAudiences, "")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing azp error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), sign(multipleAudiences, "another-api")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong azp error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), sign(jwt.Audience{audience}, "another-api")); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("single-audience wrong azp error = %v", err)
	}
	if principal, err := authenticator.Authenticate(context.Background(), sign(multipleAudiences, audience)); err != nil || principal.ID != "agent" {
		t.Fatalf("matching azp principal = %#v, error = %v", principal, err)
	}

	// Resource access tokens identify the calling client in azp while aud names
	// this API. Their azp is authorization input, not an ID-token recipient.
	authenticator.validateAuthorizedParty = false
	if principal, err := authenticator.Authenticate(context.Background(), sign(jwt.Audience{audience}, "calling-agent-client")); err != nil || principal.ID != "agent" {
		t.Fatalf("resource access-token principal = %#v, error = %v", principal, err)
	}
}

func TestSubjectClaimConfigurationRejectsUnsupportedValues(t *testing.T) {
	for _, valid := range []string{"", "sub", "oid", " oid "} {
		if _, err := validateSubjectClaim(valid); err != nil {
			t.Errorf("validateSubjectClaim(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"email", "name", "OID"} {
		if _, err := validateSubjectClaim(invalid); err == nil {
			t.Errorf("validateSubjectClaim(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestEmptyCredentialIsRejected(t *testing.T) {
	auth := NewAPIKeyOnly(&fakeLookup{})
	_, err := auth.Authenticate(context.Background(), "")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error=%v, want ErrUnauthenticated", err)
	}
}

func TestRemoteKeySetThrottlesUnknownKeysAndClassifiesOutage(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: "different-key", Algorithm: "RS256", Use: "sig",
		}}})
	}))
	defer server.Close()
	issuer := server.URL + "/issuer"
	authenticator, err := NewWithRemoteKeySet(
		context.Background(),
		issuer,
		"memory-api",
		server.URL,
		"sub",
		"",
		&fakeLookup{principal: memory.Principal{ID: "agent", Type: "agent"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       jose.JSONWebKey{Key: key, KeyID: "test-key"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   issuer,
		Subject:  "agent-subject",
		Audience: jwt.Audience{"memory-api"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if _, err := authenticator.Authenticate(context.Background(), token); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("unknown key attempt %d error=%v", attempt, err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("unknown-key attempts made %d upstream requests, want 1", got)
	}

	authenticator.transport.cooldown = 0
	unavailable.Store(true)
	var outageErr error
	deadline := time.Now().Add(time.Second)
	for requests.Load() < 2 && time.Now().Before(deadline) {
		_, outageErr = authenticator.Authenticate(context.Background(), token)
	}
	if requests.Load() < 2 {
		t.Fatal("OIDC verifier did not make the outage request")
	}
	if !errors.Is(outageErr, ErrVerifierUnavailable) {
		t.Fatalf("outage error=%v, want ErrVerifierUnavailable", outageErr)
	}
}

func TestOIDCClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	var plaintextRequests atomic.Int32
	plaintext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plaintextRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer plaintext.Close()

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, plaintext.URL, http.StatusTemporaryRedirect)
	}))
	defer tlsServer.Close()

	client, transport := newOIDCClient()
	transport.base = tlsServer.Client().Transport
	request, err := http.NewRequest(
		http.MethodPost,
		tlsServer.URL,
		strings.NewReader("code=authorization-code&client_secret=client-secret"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("OIDC client followed HTTPS-to-HTTP redirect")
	}
	if got := plaintextRequests.Load(); got != 0 {
		t.Fatalf("plaintext redirect received %d requests", got)
	}
}

func TestTokenVerifierDistinguishesInvalidCredentialsFromStorageOutages(t *testing.T) {
	secret := APIKeyPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for name, tc := range map[string]struct {
		lookupErr   error
		wantInvalid bool
	}{
		"unknown key": {lookupErr: memory.ErrNotFound, wantInvalid: true},
		"store outage": {
			lookupErr: errors.New("database unavailable"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			authenticator := NewAPIKeyOnly(&fakeLookup{apiKeyErr: tc.lookupErr})
			_, err := authenticator.TokenVerifier(context.Background(), secret, nil)
			if got := errors.Is(err, mcpauth.ErrInvalidToken); got != tc.wantInvalid {
				t.Fatalf("invalid-token classification=%v error=%v", got, err)
			}
			if !tc.wantInvalid && !errors.Is(err, ErrVerifierUnavailable) {
				t.Fatalf("store outage error=%v, want ErrVerifierUnavailable", err)
			}
		})
	}
}

type fakeLookup struct {
	principal   memory.Principal
	digest      []byte
	issuer      string
	subject     string
	expiry      time.Time
	apiKeyErr   error
	externalErr error
}

func (f *fakeLookup) ByExternalIdentity(_ context.Context, issuer, subject string) (memory.Principal, error) {
	f.issuer = issuer
	f.subject = subject
	if f.externalErr != nil {
		return memory.Principal{}, f.externalErr
	}
	return f.principal, nil
}

func (f *fakeLookup) ByAPIKeyDigest(_ context.Context, digest []byte) (memory.Principal, error) {
	f.digest = append([]byte(nil), digest...)
	if f.apiKeyErr != nil {
		return memory.Principal{}, f.apiKeyErr
	}
	return f.principal, nil
}

func (f *fakeLookup) ByAPIKeyDigestWithExpiry(
	_ context.Context,
	digest []byte,
) (memory.Principal, time.Time, error) {
	principal, err := f.ByAPIKeyDigest(context.Background(), digest)
	return principal, f.expiry, err
}
