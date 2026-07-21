package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/bytelope/zestmem/internal/buildinfo"
	"github.com/bytelope/zestmem/internal/startup"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

const (
	startupRequestTimeout       = 10 * time.Second
	startupMaxHTTPResponseBytes = 64 << 10
)

type boundedResponseTransport struct {
	base     http.RoundTripper
	deadline time.Time
}

type boundedReadCloser struct {
	reader io.Reader
	body   io.ReadCloser
	cancel context.CancelFunc
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

func (t boundedResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	requestCtx, cancel := context.WithDeadline(req.Context(), t.deadline)
	response, err := base.RoundTrip(req.Clone(requestCtx))
	if err != nil {
		cancel()
		return nil, err
	}
	if response.ContentLength > startupMaxHTTPResponseBytes {
		response.Body.Close()
		cancel()
		return nil, errors.New("startup MCP response exceeds transport limit")
	}
	response.Body = &boundedReadCloser{
		reader: io.LimitReader(response.Body, startupMaxHTTPResponseBytes),
		body:   response.Body,
		cancel: cancel,
	}
	return response, nil
}

func (body *boundedReadCloser) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *boundedReadCloser) Close() error {
	err := body.body.Close()
	body.cancel()
	return err
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	ctx := context.Background()
	endpoint := required("MEMORY_MCP_URL")
	if len(os.Args) == 2 && os.Args[1] == "--startup-context" {
		startupCtx, cancel := context.WithTimeout(ctx, startupRequestTimeout)
		defer cancel()
		httpClient, err := startupHTTPClient(startupCtx, endpoint)
		if err != nil {
			log.Fatal(err)
		}
		if err := writeStartupContext(startupCtx, endpoint, httpClient, os.Getenv("MEMORY_STARTUP_SPACE_ID"), os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
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

func startupHTTPClient(ctx context.Context, endpoint string) (*http.Client, error) {
	if os.Getenv("MEMORY_MCP_LOCAL_UNAUTHENTICATED") != "1" {
		client, err := authenticatedClient(ctx)
		if err != nil {
			return nil, err
		}
		return boundedStartupHTTPClient(client), nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse MEMORY_MCP_URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") {
		return nil, errors.New("MEMORY_MCP_LOCAL_UNAUTHENTICATED requires an HTTP loopback IP endpoint")
	}
	return boundedStartupHTTPClient(&http.Client{}), nil
}

func boundedStartupHTTPClient(base *http.Client) *http.Client {
	return &http.Client{
		Transport: boundedResponseTransport{base: base.Transport, deadline: time.Now().Add(startupRequestTimeout)},
		Timeout:   startupRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("startup MCP redirects are disabled")
		},
	}
}

func writeStartupContext(ctx context.Context, endpoint string, httpClient *http.Client, preferredSpaceID string, output io.Writer) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-memory-startup", Version: buildinfo.Version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return fmt.Errorf("connect startup client: %w", err)
	}
	defer session.Close()
	arguments := map[string]any{}
	if preferredSpaceID != "" {
		arguments["preferred_space_id"] = preferredSpaceID
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "startup_context", Arguments: arguments})
	if err != nil {
		return fmt.Errorf("call startup_context: %w", err)
	}
	if result.IsError || len(result.Content) != 1 {
		return errors.New("startup_context returned an error or unexpected content count")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.HasPrefix(text.Text, startup.Prefix) || len(text.Text) > startup.MaxContextBytes {
		return errors.New("startup_context returned an invalid bounded envelope")
	}
	if _, err := io.WriteString(output, text.Text); err != nil {
		return fmt.Errorf("write startup context: %w", err)
	}
	return nil
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
