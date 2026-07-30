package authn

import (
	"context"
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
	sign := func(audience string, expiry time.Time) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "test"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(jwt.Claims{Issuer: issuer, Subject: "agent-subject", Audience: jwt.Audience{audience}, IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(expiry)}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if p, err := auth.Authenticate(context.Background(), sign("memory-api", time.Now().Add(time.Hour))); err != nil || p.ID != "agent" {
		t.Fatalf("valid token principal=%#v error=%v", p, err)
	}
	if _, err := auth.Authenticate(context.Background(), sign("other-api", time.Now().Add(time.Hour))); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong audience error=%v", err)
	}
	if _, err := auth.Authenticate(context.Background(), sign("memory-api", time.Now().Add(-time.Hour))); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token error=%v", err)
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
	principal memory.Principal
	digest    []byte
	issuer    string
	subject   string
	expiry    time.Time
	apiKeyErr error
}

func (f *fakeLookup) ByExternalIdentity(_ context.Context, issuer, subject string) (memory.Principal, error) {
	f.issuer = issuer
	f.subject = subject
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
