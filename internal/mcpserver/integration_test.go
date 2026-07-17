package mcpserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/mcpserver"
	"github.com/bytelope/zestmem/internal/memory"
	"github.com/bytelope/zestmem/internal/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func TestStreamableHTTPListsAndCallsAllTools(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	owner, agent, space := uuid.NewString(), uuid.NewString(), uuid.NewString()
	secret := "am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	digest := sha256.Sum256([]byte(secret))
	_, err = conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES($1,'human','owner'),($2,'agent','agent')`, owner, agent)
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(id,type,name,slug) VALUES($1,'organization','Org','org')`, space)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'contributor',$3)`, agent, space, owner)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO api_keys(principal_id,prefix,digest,created_by) VALUES($1,'integr-test',$2,$3)`, agent, digest[:], owner)
	}
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ByAPIKeyDigest(ctx, digest[:]); err != nil {
		t.Fatalf("fallback-key lookup: %v", err)
	}
	domain := memory.NewService(store, store)
	server := httptest.NewServer(authn.NewAPIKeyOnly(store).Middleware(mcpserver.New(domain).Handler()))
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: secret}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 6 {
		t.Fatalf("tool count=%d", len(listed.Tools))
	}
	var rememberRequired []string
	for _, tool := range listed.Tools {
		if tool.Name != "remember" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		rememberRequired = schema.Required
	}
	if !slices.Contains(rememberRequired, "target_space_id") {
		t.Fatalf("remember required fields=%v", rememberRequired)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("catalog result=%#v", result)
	}
	bodyMarker := "SECRET-BODY-MUST-NOT-APPEAR-IN-MUTATION-RESULT"
	rememberArgs := map[string]any{"target_space_id": space, "title": "MCP mutation context test", "body": bodyMarker, "kind": "fact", "reason": "integration test", "idempotency_key": "mcp-create-1"}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: rememberArgs})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("remember result=%#v", result)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; strings.Contains(text, bodyMarker) {
		t.Fatalf("mutation result leaked body: %s", text)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: rememberArgs})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("remember replay result=%#v", result)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"written":false`) || !strings.Contains(text, `"replayed":true`) {
		t.Fatalf("remember replay flags missing: %s", text)
	}
	rememberArgs["idempotency_key"] = "mcp-create-2"
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: rememberArgs})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("exact duplicate was not a tool error")
	}
	if text := result.Content[0].(*mcp.TextContent).Text; strings.Contains(text, bodyMarker) {
		t.Fatalf("duplicate result leaked body: %s", text)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	status := func() int {
		req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	_, err = admin.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE principal_id=$1`, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d", got)
	}
	_, err = admin.Exec(ctx, `UPDATE api_keys SET revoked_at=NULL,expires_at=now()-interval '1 minute' WHERE principal_id=$1`, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("expired key status=%d", got)
	}
	_, err = admin.Exec(ctx, `UPDATE api_keys SET expires_at=NULL WHERE principal_id=$1`, agent)
	if err == nil {
		_, err = admin.Exec(ctx, `UPDATE principals SET disabled_at=now() WHERE id=$1`, agent)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("disabled principal status=%d", got)
	}
}
