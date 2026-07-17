package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/bytelope/zestmem/internal/buildinfo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

type azureTokenSource struct {
	ctx        context.Context
	credential azcore.TokenCredential
	scope      string
}

func (s azureTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.credential.GetToken(s.ctx, policy.TokenRequestOptions{Scopes: []string{s.scope}})
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: token.Token, TokenType: "Bearer", Expiry: token.ExpiresOn}, nil
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	ctx := context.Background()
	endpoint := required("MEMORY_MCP_URL")
	httpClient, err := authenticatedClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	remoteClient := mcp.NewClient(&mcp.Implementation{Name: "agent-memory-bridge", Version: buildinfo.Version}, nil)
	remote, err := remoteClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer remote.Close()
	listed, err := remote.ListTools(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	local := mcp.NewServer(&mcp.Implementation{Name: "agent-memory-bridge", Version: buildinfo.Version}, nil)
	for _, tool := range listed.Tools {
		tool := tool
		name := tool.Name
		local.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return remote.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
		})
	}
	if err := local.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

func authenticatedClient(ctx context.Context) (*http.Client, error) {
	if token := os.Getenv("MEMORY_TOKEN"); token != "" {
		return &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}, nil
	}
	scope := os.Getenv("MEMORY_SCOPE")
	if scope == "" {
		audience := strings.TrimSuffix(os.Getenv("MEMORY_AUDIENCE"), "/")
		if audience == "" {
			return nil, errors.New("MEMORY_SCOPE or MEMORY_AUDIENCE is required")
		}
		scope = audience + "/.default"
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, errors.New("initialize Azure credential: " + err.Error())
	}
	tokens := oauth2.ReuseTokenSource(nil, azureTokenSource{ctx: ctx, credential: credential, scope: scope})
	return oauth2.NewClient(ctx, tokens), nil
}

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}
