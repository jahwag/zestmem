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
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jahwag/zestmem/internal/memory"
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

func TestEmptyCredentialIsRejected(t *testing.T) {
	auth := NewAPIKeyOnly(&fakeLookup{})
	_, err := auth.Authenticate(context.Background(), "")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error=%v, want ErrUnauthenticated", err)
	}
}

type fakeLookup struct {
	principal memory.Principal
	digest    []byte
}

func (f *fakeLookup) ByExternalIdentity(context.Context, string, string) (memory.Principal, error) {
	return f.principal, nil
}

func (f *fakeLookup) ByAPIKeyDigest(_ context.Context, digest []byte) (memory.Principal, error) {
	f.digest = append([]byte(nil), digest...)
	return f.principal, nil
}
