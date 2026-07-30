package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
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
		}, nil
	}
	mux := http.NewServeMux()
	registerMCPRoutes(
		mux,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
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
