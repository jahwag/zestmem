package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jahwag/zestmem/internal/startup"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type startupContextInput struct {
	PreferredSpaceID string `json:"preferred_space_id,omitempty"`
}

func TestValidateEndpointRequiresHTTPSOrLoopback(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		ok       bool
	}{
		{"https://memory.example.com/mcp", true},
		{"http://127.0.0.1:14501/mcp", true},
		{"http://[::1]:14501/mcp", true},
		{"http://localhost:14501/mcp", true},
		{"http://memory.example.com/mcp", false},
		{"https://user:secret@memory.example.com/mcp", false},
		{"https://memory.example.com/mcp?token=secret", false},
		{"https://memory.example.com/mcp#fragment", false},
		{"", false},
	} {
		err := validateEndpoint(tc.endpoint)
		if tc.ok && err != nil {
			t.Errorf("validateEndpoint(%q) = %v", tc.endpoint, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("validateEndpoint(%q) unexpectedly succeeded", tc.endpoint)
		}
	}
}

func TestAuthenticatedClientRejectsRedirectWithoutForwardingToken(t *testing.T) {
	t.Setenv("MEMORY_TOKEN", "native-memory-token")
	t.Setenv("MEMORY_SCOPE", "")
	t.Setenv("MEMORY_AUDIENCE", "")
	targetHit := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()

	client, err := authenticatedClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(redirect.URL); err == nil {
		t.Fatal("authenticated client followed redirect")
	}
	select {
	case authorization := <-targetHit:
		t.Fatalf("redirect target received Authorization %q", authorization)
	default:
	}
}

func TestEntraClientRequiresPinnedCredentialChain(t *testing.T) {
	t.Setenv("MEMORY_TOKEN", "")
	t.Setenv("MEMORY_SCOPE", "api://memory/.default")
	t.Setenv("MEMORY_AUDIENCE", "")
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "")
	if _, err := authenticatedClient(context.Background()); err == nil {
		t.Fatal("missing AZURE_TOKEN_CREDENTIALS did not fail closed")
	}
}

func TestWriteStartupContext(t *testing.T) {
	want := startup.Prefix + `{"spaces":[],"omitted_count":0}` + "\n"
	var preferred string
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "startup_context"},
		func(_ context.Context, _ *mcp.CallToolRequest, input startupContextInput) (*mcp.CallToolResult, any, error) {
			preferred = input.PreferredSpaceID
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: want}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	var output bytes.Buffer
	err := writeStartupContext(context.Background(), httpServer.URL, httpServer.Client(), "49400172-ec60-4646-92c8-331d1db78a4a", &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != want || preferred != "49400172-ec60-4646-92c8-331d1db78a4a" {
		t.Fatalf("output=%q preferred=%q", output.String(), preferred)
	}
}

func TestWriteStartupContextRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		content []mcp.Content
		isError bool
	}{
		{name: "wrong prefix", content: []mcp.Content{&mcp.TextContent{Text: "untrusted"}}},
		{name: "over limit", content: []mcp.Content{&mcp.TextContent{Text: startup.Prefix + strings.Repeat("x", startup.MaxContextBytes-len(startup.Prefix)+1)}}},
		{name: "multiple blocks", content: []mcp.Content{&mcp.TextContent{Text: startup.Prefix}, &mcp.TextContent{Text: "extra"}}},
		{name: "tool error", content: []mcp.Content{&mcp.TextContent{Text: "failed"}}, isError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "startup_context"},
				func(context.Context, *mcp.CallToolRequest, startupContextInput) (*mcp.CallToolResult, any, error) {
					return &mcp.CallToolResult{Content: test.content, IsError: test.isError}, nil, nil
				})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
			httpServer := httptest.NewServer(handler)
			defer httpServer.Close()
			if err := writeStartupContext(context.Background(), httpServer.URL, httpServer.Client(), "", &bytes.Buffer{}); err == nil {
				t.Fatal("expected invalid startup envelope to fail")
			}
		})
	}
}

func TestStartupHTTPClientRestrictsUnauthenticatedEndpoint(t *testing.T) {
	t.Setenv("MEMORY_MCP_LOCAL_UNAUTHENTICATED", "1")
	for _, endpoint := range []string{"http://127.0.0.1:14501/mcp", "http://[::1]:14501/mcp"} {
		if _, err := startupHTTPClient(context.Background(), endpoint); err != nil {
			t.Errorf("startupHTTPClient(%q): %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"https://127.0.0.1:14501/mcp",
		"http://localhost:14501/mcp",
		"http://example.com/mcp",
		"http://user@127.0.0.1:14501/mcp",
	} {
		if _, err := startupHTTPClient(context.Background(), endpoint); err == nil {
			t.Errorf("startupHTTPClient(%q) accepted unsafe endpoint", endpoint)
		}
	}
}

func TestStartupHTTPClientRejectsRedirectsAndBoundsResponses(t *testing.T) {
	t.Setenv("MEMORY_MCP_LOCAL_UNAUTHENTICATED", "1")
	targetHit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHit <- struct{}{}
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	client, err := startupHTTPClient(context.Background(), redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, redirect.URL, strings.NewReader("request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); err == nil {
		t.Fatal("expected startup client to reject redirect")
	}
	select {
	case <-targetHit:
		t.Fatal("startup client followed redirect")
	default:
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", "65537")
		response.WriteHeader(http.StatusOK)
	}))
	defer oversized.Close()
	client, err = startupHTTPClient(context.Background(), oversized.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(oversized.URL); err == nil {
		t.Fatal("expected declared oversized response to fail")
	}

	chunked := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, strings.Repeat("x", startupMaxHTTPResponseBytes+1024))
	}))
	defer chunked.Close()
	client, err = startupHTTPClient(context.Background(), chunked.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(chunked.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != startupMaxHTTPResponseBytes {
		t.Fatalf("bounded body length=%d", len(body))
	}
}

func TestWriteStartupContextHonorsTotalDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = boundedResponseTransport{base: client.Transport, deadline: time.Now().Add(50 * time.Millisecond)}
	started := time.Now()
	if err := writeStartupContext(context.Background(), server.URL, client, "", &bytes.Buffer{}); err == nil {
		t.Fatal("expected deadline failure")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("startup deadline took %s", elapsed)
	}
}
