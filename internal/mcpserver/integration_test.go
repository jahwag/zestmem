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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/mcpserver"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/jahwag/zestmem/internal/postgres"
	"github.com/jahwag/zestmem/internal/startup"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	host  string
}

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	vector := make([]float32, 384)
	vector[0] = 1
	return vector, nil
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.host != "" {
		clone.Host = t.host
	}
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
	owner, agent, space, workspaceProject := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	spaceCue := "Working on MCP integration behavior."
	secret := "am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	digest := sha256.Sum256([]byte(secret))
	_, err = conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES($1,'human','owner'),($2,'agent','agent')`, owner, agent)
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(id,type,name,slug,use_when) VALUES($1,'organization','Org','org',$2)`, space, spaceCue)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'contributor',$3)`, agent, space, owner)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(id,type,parent_id,name,slug) VALUES($1,'project',$2,'Mapped project','mapped-project')`, workspaceProject, space)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO spaces(type,parent_id,name,slug)
	SELECT 'project',$1,repeat('Catalog child ',6)||n::text,'catalog-child-'||n::text FROM generate_series(1,19) n`, space)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `INSERT INTO workspace_bindings(project_space_id,claim_kind,normalized_value,created_by)
	 VALUES($1,'git_remote','https://example.invalid/team/project',$2)`, workspaceProject, owner)
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
	domain := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	authenticator := authn.NewAPIKeyOnly(store)
	handler := auth.RequireBearerToken(
		authenticator.TokenVerifier,
		&auth.RequireBearerTokenOptions{AllowMissingExpiration: true},
	)(mcpserver.New(domain).WithStartupApprovals(
		startup.Approvals{space: startup.CueHash(spaceCue)},
	).Handler())
	server := httptest.NewServer(handler)
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
	if len(listed.Tools) != 11 {
		t.Fatalf("tool count=%d", len(listed.Tools))
	}
	var rememberRequired []string
	var reviseSchema, startupSchema, reverieSchema string
	for _, tool := range listed.Tools {
		if tool.Name == "revise_memory" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			reviseSchema = string(raw)
		}
		if tool.Name == "startup_context" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			startupSchema = string(raw)
		}
		if tool.Name == "reverie" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			reverieSchema = string(raw)
		}
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
	if strings.Contains(startupSchema, `"cursor"`) || !strings.Contains(startupSchema, `"preferred_space_id"`) {
		t.Fatalf("startup context exposed wrong pagination schema: %s", startupSchema)
	}
	var reverieInputSchema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal([]byte(reverieSchema), &reverieInputSchema); err != nil {
		t.Fatal(err)
	}
	if len(reverieInputSchema.Properties) != 1 || reverieInputSchema.Properties["cue"] == nil || !slices.Equal(reverieInputSchema.Required, []string{"cue"}) {
		t.Fatalf("reverie input schema=%s", reverieSchema)
	}
	for _, phrase := range []string{"explicitly pass its current value to preserve it", "empty string to clear it", "240 UTF-8 bytes", "untrusted"} {
		if !strings.Contains(reviseSchema, phrase) {
			t.Fatalf("revise use_when schema omitted %q: %s", phrase, reviseSchema)
		}
	}
	workspaceClaims := []map[string]any{{"kind": "git_remote", "value": "https://example.invalid/team/project"}}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog", Arguments: map[string]any{"workspace_claims": workspaceClaims}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("catalog result=%#v", result)
	}
	var firstCatalog memory.CatalogResult
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &firstCatalog); err != nil || firstCatalog.NextCursor == "" || len(firstCatalog.Spaces) == 0 {
		t.Fatalf("first catalog page=%#v error=%v", firstCatalog, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "catalog", Arguments: map[string]any{"cursor": firstCatalog.NextCursor, "workspace_claims": workspaceClaims}})
	if err != nil || result.IsError {
		t.Fatalf("second catalog result=%#v error=%v", result, err)
	}
	var secondCatalog memory.CatalogResult
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &secondCatalog); err != nil || len(secondCatalog.Spaces) == 0 || secondCatalog.Spaces[0].SpaceID == firstCatalog.Spaces[0].SpaceID {
		t.Fatalf("second catalog page=%#v error=%v", secondCatalog, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "startup_context", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("startup context result=%#v error=%v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, spaceCue) || !strings.Contains(text, "untrusted routing metadata") || len(text) > startup.MaxContextBytes {
		t.Fatalf("startup context=%q", text)
	}
	for _, call := range []*mcp.CallToolParams{
		{Name: "suggest_topics", Arguments: map[string]any{"query": "sec"}},
		{Name: "list_provenance", Arguments: map[string]any{"field": "class"}},
	} {
		result, err = session.CallTool(ctx, call)
		if err != nil || result.IsError {
			t.Fatalf("%s result=%#v err=%v", call.Name, result, err)
		}
	}
	bodyMarker := "SECRET-BODY-MUST-NOT-APPEAR-IN-MUTATION-RESULT"
	useWhen := "Testing MCP mutation result context boundaries."
	rememberArgs := map[string]any{"target_space_id": space, "title": "MCP mutation context test", "body": strings.Repeat("ordinary line\n", 100) + bodyMarker, "use_when": useWhen, "kind": "fact", "reason": "integration test", "idempotency_key": "mcp-create-1"}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: rememberArgs})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("remember result=%#v", result)
	}
	createdText := result.Content[0].(*mcp.TextContent).Text
	if strings.Contains(createdText, bodyMarker) {
		t.Fatalf("mutation result leaked body: %s", createdText)
	} else if !strings.Contains(createdText, useWhen) {
		t.Fatalf("mutation result omitted use_when: %s", createdText)
	}
	var createdPayload struct {
		Memory struct {
			ID string `json:"id"`
		} `json:"memory"`
	}
	if err := json.Unmarshal([]byte(createdText), &createdPayload); err != nil || createdPayload.Memory.ID == "" {
		t.Fatalf("decode remember result: id=%q error=%v", createdPayload.Memory.ID, err)
	}
	var created struct {
		Memory struct {
			ID string `json:"id"`
		} `json:"memory"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &created); err != nil || created.Memory.ID == "" {
		t.Fatalf("created memory result=%#v err=%v", result, err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "read_memory", Arguments: map[string]any{"memory_id": created.Memory.ID}})
	if err != nil || result.IsError {
		t.Fatalf("full read result=%#v err=%v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, bodyMarker) || !strings.Contains(text, `"content_hash"`) {
		t.Fatalf("full read shape changed: %s", text)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "read_memory_segment", Arguments: map[string]any{"memory_id": created.Memory.ID}})
	if err != nil || result.IsError {
		t.Fatalf("bounded read result=%#v err=%v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; strings.Contains(text, bodyMarker) || !strings.Contains(text, `"next_start_line":81`) || !strings.Contains(text, `"use_when":"Testing MCP mutation result context boundaries."`) || strings.Contains(text, `"content_hash"`) {
		t.Fatalf("segment read was not bounded: %s", text)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "read_memory_segment", Arguments: map[string]any{"memory_id": created.Memory.ID, "pattern": "SECRET-BODY"}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, bodyMarker) || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `"use_when":"Testing MCP mutation result context boundaries."`) {
		t.Fatalf("grep read result=%#v err=%v", result, err)
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
	revisedUseWhen := "Reviewing MCP mutation result context boundaries."
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "revise_memory", Arguments: map[string]any{
		"memory_id": createdPayload.Memory.ID, "expected_revision": 1,
		"changed_fields": map[string]any{"use_when": revisedUseWhen},
		"reason":         "integration test cue revision", "idempotency_key": "mcp-revise-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, revisedUseWhen) {
		t.Fatalf("revise use_when result=%#v", result)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{"query": "MCP mutation context test"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, revisedUseWhen) {
		t.Fatalf("recall omitted revised use_when: %#v", result)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "reverie", Arguments: map[string]any{"cue": "MCP mutation context"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, revisedUseWhen) {
		t.Fatalf("reverie omitted revised use_when: %#v", result)
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

func TestAuthenticatedMCPIsolatesPrincipalsAndSpaces(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`)
	if err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	ownerID := uuid.NewString()
	aliceID := uuid.NewString()
	bobID := uuid.NewString()
	aliceSpaceID := uuid.NewString()
	bobSpaceID := uuid.NewString()
	aliceToken := "am_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	bobToken := "am_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	aliceDigest := sha256.Sum256([]byte(aliceToken))
	bobDigest := sha256.Sum256([]byte(bobToken))
	_, err = conn.Exec(ctx, `
		INSERT INTO principals(id,type,display_name)
		VALUES($1,'human','owner'),($2,'agent','alice'),($3,'agent','bob')`,
		ownerID, aliceID, bobID)
	if err == nil {
		_, err = conn.Exec(ctx, `
			INSERT INTO spaces(id,type,name,slug)
			VALUES($1,'organization','Alice space','alice-space'),
			      ($2,'organization','Bob space','bob-space')`,
			aliceSpaceID, bobSpaceID)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `
			INSERT INTO space_grants(principal_id,space_id,role,granted_by)
			VALUES($1,$2,'contributor',$3),($4,$5,'contributor',$3)`,
			aliceID, aliceSpaceID, ownerID, bobID, bobSpaceID)
	}
	if err == nil {
		_, err = conn.Exec(ctx, `
			INSERT INTO api_keys(principal_id,prefix,digest,created_by)
			VALUES($1,'alice-key',$2,$3),($4,'bob-key',$5,$3)`,
			aliceID, aliceDigest[:], ownerID, bobID, bobDigest[:])
	}
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	domain := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	authenticator := authn.NewAPIKeyOnly(store)
	handler := auth.RequireBearerToken(
		authenticator.TokenVerifier,
		&auth.RequireBearerTokenOptions{AllowMissingExpiration: true},
	)(mcpserver.New(domain).Handler())
	server := httptest.NewServer(handler)
	defer server.Close()

	connect := func(token string) *mcp.ClientSession {
		t.Helper()
		session, err := mcp.NewClient(
			&mcp.Implementation{Name: "isolation-test", Version: "1"},
			nil,
		).Connect(
			ctx,
			&mcp.StreamableClientTransport{
				Endpoint: server.URL,
				HTTPClient: &http.Client{Transport: bearerTransport{
					token: token,
					host:  "zestmem.example.test",
				}},
				DisableStandaloneSSE: true,
			},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	aliceSession := connect(aliceToken)
	defer aliceSession.Close()
	bobSession := connect(bobToken)
	defer bobSession.Close()

	assertCatalog := func(session *mcp.ClientSession, visibleID, hiddenID string) {
		t.Helper()
		result, err := session.CallTool(
			ctx,
			&mcp.CallToolParams{Name: "catalog", Arguments: map[string]any{}},
		)
		if err != nil || result.IsError {
			t.Fatalf("catalog result=%#v error=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, visibleID) || strings.Contains(text, hiddenID) {
			t.Fatalf("catalog crossed space boundary: %s", text)
		}
	}
	assertCatalog(aliceSession, aliceSpaceID, bobSpaceID)
	assertCatalog(bobSession, bobSpaceID, aliceSpaceID)

	remember := func(session *mcp.ClientSession, spaceID, key, marker string) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "remember",
			Arguments: map[string]any{
				"target_space_id": spaceID,
				"title":           marker,
				"body":            marker,
				"use_when":        "Testing authenticated MCP isolation.",
				"kind":            "fact",
				"reason":          "integration test",
				"idempotency_key": key,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	aliceMemory := remember(aliceSession, aliceSpaceID, "alice-own-memory", "ALICE-ONLY-MARKER")
	if aliceMemory.IsError {
		t.Fatalf("alice own-space write failed: %#v", aliceMemory)
	}
	if crossWrite := remember(aliceSession, bobSpaceID, "alice-cross-write", "CROSS-WRITE"); !crossWrite.IsError {
		t.Fatalf("alice wrote to bob's space: %#v", crossWrite)
	}
	var created struct {
		Memory struct {
			ID string `json:"id"`
		} `json:"memory"`
	}
	if err := json.Unmarshal(
		[]byte(aliceMemory.Content[0].(*mcp.TextContent).Text),
		&created,
	); err != nil || created.Memory.ID == "" {
		t.Fatalf("decode alice memory: id=%q error=%v", created.Memory.ID, err)
	}
	bobRead, err := bobSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_memory",
		Arguments: map[string]any{"memory_id": created.Memory.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bobRead.IsError {
		t.Fatalf("bob read alice's memory: %#v", bobRead)
	}
}
