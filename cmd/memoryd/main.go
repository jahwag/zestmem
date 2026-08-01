package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/buildinfo"
	"github.com/jahwag/zestmem/internal/console"
	"github.com/jahwag/zestmem/internal/embed"
	"github.com/jahwag/zestmem/internal/httpx"
	"github.com/jahwag/zestmem/internal/mcpserver"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/jahwag/zestmem/internal/postgres"
	"github.com/jahwag/zestmem/internal/startup"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		resp, err := http.Get("http://127.0.0.1:8080/readyz")
		if err != nil || resp.StatusCode != http.StatusNoContent {
			os.Exit(1)
		}
		resp.Body.Close()
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	databaseURL := required("DATABASE_URL")
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("database open", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.VerifyEmbeddingFingerprint(ctx); err != nil {
		slog.Error("embedding fingerprint", "error", err)
		os.Exit(1)
	}
	var authenticator *authn.Authenticator
	issuer, audience := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_AUDIENCE")
	if issuer != "" || audience != "" {
		if issuer == "" || audience == "" {
			slog.Error("OIDC_ISSUER and OIDC_AUDIENCE must be set together")
			os.Exit(1)
		}
		authenticator, err = authn.NewWithOptions(
			ctx,
			issuer,
			audience,
			env("OIDC_SUBJECT_CLAIM", "sub"),
			os.Getenv("OIDC_REQUIRED_ROLE"),
			store,
		)
		if err != nil {
			slog.Error("OIDC discovery", "error", err)
			os.Exit(1)
		}
	} else {
		authenticator = authn.NewAPIKeyOnly(store)
	}
	var consoleAssertion *authn.Authenticator
	assertionIssuer := os.Getenv("CONSOLE_ASSERTION_ISSUER")
	assertionAudience := os.Getenv("CONSOLE_ASSERTION_AUDIENCE")
	assertionJWKSURL := os.Getenv("CONSOLE_ASSERTION_JWKS_URL")
	if err := validateConsoleAuthoritySeparation(
		issuer,
		audience,
		assertionIssuer,
		assertionAudience,
		os.Getenv("CONSOLE_OIDC_ISSUER"),
		os.Getenv("CONSOLE_OIDC_CLIENT_ID"),
	); err != nil {
		slog.Error("configure console authority separation", "error", err)
		os.Exit(2)
	}
	if assertionIssuer != "" || assertionAudience != "" || assertionJWKSURL != "" {
		if assertionIssuer == "" || assertionAudience == "" || assertionJWKSURL == "" {
			slog.Error("CONSOLE_ASSERTION_ISSUER, CONSOLE_ASSERTION_AUDIENCE, and CONSOLE_ASSERTION_JWKS_URL must be set together")
			os.Exit(2)
		}
		consoleAssertion, err = authn.NewWithRemoteKeySet(
			ctx,
			assertionIssuer,
			assertionAudience,
			assertionJWKSURL,
			env("CONSOLE_ASSERTION_SUBJECT_CLAIM", "sub"),
			os.Getenv("CONSOLE_ASSERTION_REQUIRED_ROLE"),
			store,
		)
		if err != nil {
			slog.Error("configure console assertion authentication", "error", err)
			os.Exit(2)
		}
	}
	consolePublicOrigin := os.Getenv("CONSOLE_PUBLIC_ORIGIN")
	consoleSessions, err := authn.NewBrowserSessions(
		authenticator,
		consoleAssertion,
		env("CONSOLE_ASSERTION_HEADER", "Cf-Access-Jwt-Assertion"),
		consolePublicOrigin,
	)
	if err != nil {
		slog.Error("configure console authentication", "error", err)
		os.Exit(2)
	}
	consoleOIDC, err := newConsoleBrowserOIDC(ctx, consolePublicOrigin, store)
	if err != nil {
		slog.Error("configure console OIDC", "error", err)
		os.Exit(2)
	}
	if consoleOIDC != nil {
		consoleSessions.WithOIDC(consoleOIDC)
	}
	metrics := &httpx.Metrics{}
	startupApprovals, err := startup.ParseApprovals(os.Getenv("ZESTMEM_STARTUP_CUE_APPROVALS"))
	if err != nil {
		slog.Error("startup cue approvals", "error", err)
		os.Exit(1)
	}
	domain := memory.NewService(store, store)
	tei := embed.NewTEI(required("TEI_URL"), 15*time.Second)
	domain.WithEmbedder(embed.NewObserved(embed.NewLimited(tei, envInt("TEI_CONCURRENCY", 4)), metrics))
	mcpMemoryServer := mcpserver.New(domain, metrics).WithStartupApprovals(startupApprovals)
	mcpHandler := mcpMemoryServer.Handler()
	limiter := httpx.NewLimiter(envInt("RATE_LIMIT_PER_MINUTE", 120), envInt("RATE_LIMIT_BURST", 20), metrics)
	consoleAuthLimiter := httpx.NewLimiter(
		envInt("CONSOLE_AUTH_RATE_LIMIT_PER_MINUTE", 30),
		envInt("CONSOLE_AUTH_RATE_LIMIT_BURST", 10),
		metrics,
	)
	consoleSessions.WithLoginAdmission(consoleAuthLimiter.AdmitUnauthenticated)
	protect := func(next http.Handler) http.Handler {
		return protectConsoleAPI(consoleSessions, authenticator, limiter, next)
	}
	mux := http.NewServeMux()
	mcpResourceURI := env("MCP_RESOURCE_URI", "http://127.0.0.1:8080/mcp")
	mcpResourceMetadataURL := env(
		"MCP_RESOURCE_METADATA_URL",
		strings.TrimSuffix(mcpResourceURI, "/mcp")+"/.well-known/oauth-protected-resource/mcp",
	)
	registerMCPRoutes(
		mux,
		limiter.Middleware(http.MaxBytesHandler(mcpHandler, 1<<20)),
		authenticator.TokenVerifier,
		issuer,
		mcpResourceURI,
		mcpResourceMetadataURL,
		strings.Fields(os.Getenv("OIDC_SCOPES")),
	)
	consoleHandler := console.Handler(domain, protect)
	mux.Handle("/console/", consoleHandler)
	mux.Handle("/api/v1/", consoleHandler)
	registerConsoleAuthRoutes(mux, consoleSessions)
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusTemporaryRedirect)
	})
	bootstrapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", 401)
			return
		}
		var claims []memory.WorkspaceClaim
		if value := r.URL.Query().Get("git_remote"); value != "" {
			claims = append(claims, memory.WorkspaceClaim{Kind: "git_remote", Value: value})
		}
		if value := r.URL.Query().Get("local_path"); value != "" {
			claims = append(claims, memory.WorkspaceClaim{Kind: "local_path", Value: value})
		}
		result, err := domain.Catalog(r.Context(), p, claims)
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(startup.ApprovedCatalog(result, startupApprovals))
	})
	mux.Handle("/bootstrap/catalog", authenticator.Middleware(limiter.Middleware(bootstrapHandler)))
	bootstrapContextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", 401)
			return
		}
		result, err := domain.StartupCatalog(r.Context(), p, nil)
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		preferredSpaceID := r.URL.Query().Get("space_id")
		if preferredSpaceID, err = canonicalSpaceID(preferredSpaceID); err != nil {
			http.Error(w, "invalid preferred Space", http.StatusBadRequest)
			return
		}
		contextText, err := startup.Render(result, startupApprovals, preferredSpaceID)
		if err != nil {
			http.Error(w, "startup context unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(contextText))
	})
	mux.Handle("/bootstrap/context", authenticator.Middleware(limiter.Middleware(bootstrapContextHandler)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		if err := tei.Ready(r.Context()); err != nil {
			http.Error(w, "embedding service unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/metrics", metrics.Handler())
	server := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: httpx.Observe(mux, metrics), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("memory server listening", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func protectConsoleAPI(
	sessions *authn.BrowserSessions,
	authenticator *authn.Authenticator,
	limiter *httpx.Limiter,
	next http.Handler,
) http.Handler {
	limited := limiter.Middleware(next)
	browser := sessions.Middleware(limited)
	bearer := authenticator.Middleware(limited)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, value := range r.Header.Values("Authorization") {
			if strings.TrimSpace(value) != "" {
				bearer.ServeHTTP(w, r)
				return
			}
		}
		browser.ServeHTTP(w, r)
	})
}

func validateConsoleAuthoritySeparation(
	resourceIssuer string,
	resourceAudience string,
	assertionIssuer string,
	assertionAudience string,
	browserIssuer string,
	browserClientID string,
) error {
	resourceIssuer = strings.TrimSpace(resourceIssuer)
	resourceAudience = strings.TrimSpace(resourceAudience)
	if resourceIssuer == "" || resourceAudience == "" {
		return nil
	}
	if resourceIssuer == strings.TrimSpace(assertionIssuer) &&
		resourceAudience == strings.TrimSpace(assertionAudience) {
		return errors.New("console assertion issuer and audience must differ from the MCP OIDC authority")
	}
	if resourceIssuer == strings.TrimSpace(browserIssuer) &&
		resourceAudience == strings.TrimSpace(browserClientID) {
		return errors.New("console OIDC issuer and client ID must differ from the MCP OIDC authority")
	}
	return nil
}

func newConsoleBrowserOIDC(
	ctx context.Context,
	publicOrigin string,
	lookup authn.PrincipalLookup,
) (*authn.BrowserOIDC, error) {
	issuer := strings.TrimSpace(os.Getenv("CONSOLE_OIDC_ISSUER"))
	clientID := strings.TrimSpace(os.Getenv("CONSOLE_OIDC_CLIENT_ID"))
	if issuer == "" && clientID == "" {
		return nil, nil
	}
	if issuer == "" || clientID == "" {
		return nil, errors.New("CONSOLE_OIDC_ISSUER and CONSOLE_OIDC_CLIENT_ID must be set together")
	}
	publicOrigin = strings.TrimSuffix(strings.TrimSpace(publicOrigin), "/")
	if publicOrigin == "" {
		return nil, errors.New("CONSOLE_PUBLIC_ORIGIN is required for browser OIDC")
	}
	expectedRedirectURL := publicOrigin + "/console/auth/oidc/callback"
	redirectURL := strings.TrimSpace(os.Getenv("CONSOLE_OIDC_REDIRECT_URL"))
	if redirectURL == "" {
		redirectURL = expectedRedirectURL
	} else if redirectURL != expectedRedirectURL {
		return nil, errors.New("CONSOLE_OIDC_REDIRECT_URL must match CONSOLE_PUBLIC_ORIGIN callback")
	}
	clientSecret, err := loadOptionalSecret("CONSOLE_OIDC_CLIENT_SECRET", "CONSOLE_OIDC_CLIENT_SECRET_FILE")
	if err != nil {
		return nil, err
	}
	return authn.NewBrowserOIDC(
		ctx,
		issuer,
		clientID,
		clientSecret,
		redirectURL,
		strings.Fields(os.Getenv("CONSOLE_OIDC_SCOPES")),
		env("CONSOLE_OIDC_SUBJECT_CLAIM", "sub"),
		os.Getenv("CONSOLE_OIDC_REQUIRED_ROLE"),
		lookup,
	)
}

func loadOptionalSecret(valueEnv, fileEnv string) (string, error) {
	const (
		maxSecretBytes     = 4096
		maxSecretFileBytes = maxSecretBytes + 2 // Permit one trailing CRLF.
	)

	value := strings.TrimSpace(os.Getenv(valueEnv))
	path := strings.TrimSpace(os.Getenv(fileEnv))
	if value != "" && path != "" {
		return "", fmt.Errorf("%s and %s are mutually exclusive", valueEnv, fileEnv)
	}
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
		if err != nil {
			return "", err
		}
		if len(raw) > maxSecretFileBytes {
			return "", fmt.Errorf("%s must contain at most %d secret bytes with an optional line ending", fileEnv, maxSecretBytes)
		}
		value = strings.TrimSpace(string(raw))
	}
	if len(value) > maxSecretBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("%s must be at most %d bytes without newlines", valueEnv, maxSecretBytes)
	}
	return value, nil
}

func registerConsoleAuthRoutes(mux *http.ServeMux, sessions *authn.BrowserSessions) {
	mux.HandleFunc("POST /console/auth/token", sessions.LoginHandler)
	mux.Handle("GET /console/auth/session", sessions.Middleware(http.HandlerFunc(sessions.SessionHandler)))
	mux.HandleFunc("GET /console/auth/config", sessions.AuthConfigHandler)
	mux.HandleFunc("GET /console/auth/oidc/start", sessions.OIDCStartHandler)
	mux.HandleFunc("GET /console/auth/oidc/callback", sessions.OIDCCallbackHandler)
	mux.HandleFunc("POST /console/auth/logout", sessions.LogoutHandler)
}

func registerMCPRoutes(
	mux *http.ServeMux,
	handler http.Handler,
	verifier auth.TokenVerifier,
	issuer string,
	resourceURI string,
	resourceMetadataURL string,
	scopes []string,
) {
	options := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if issuer != "" {
		options.ResourceMetadataURL = resourceMetadataURL
		metadata := &oauthex.ProtectedResourceMetadata{
			Resource:             resourceURI,
			AuthorizationServers: []string{issuer},
			ScopesSupported:      scopes,
		}
		mux.Handle(
			"GET /.well-known/oauth-protected-resource/mcp",
			auth.ProtectedResourceMetadataHandler(metadata),
		)
	}
	mux.Handle("/mcp", auth.RequireBearerToken(verifier, options)(handler))
}

func canonicalSpaceID(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		slog.Error("missing required environment variable", "name", name)
		os.Exit(2)
	}
	return value
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}
