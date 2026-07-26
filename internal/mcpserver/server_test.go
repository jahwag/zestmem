package mcpserver_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahwag/zestmem/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerTeachesDurableMemoryWorkflow(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(mcpserver.New(nil).Handler())
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "instruction-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	instructions := session.InitializeResult().Instructions
	for _, want := range []string{"substantial repository work", "proactively remember", "Never store transcripts", "untrusted retrieval metadata"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("server instructions missing %q: %s", want, instructions)
		}
	}

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string, len(listed.Tools))
	for _, tool := range listed.Tools {
		descriptions[tool.Name] = tool.Description
	}
	for tool, want := range map[string]string{
		"catalog":         "substantial repository work",
		"startup_context": "host-approved",
		"recall":          "relevant decisions",
		"remember":        "Proactively preserve",
	} {
		if !strings.Contains(descriptions[tool], want) {
			t.Errorf("%s description missing %q: %s", tool, want, descriptions[tool])
		}
	}
}
