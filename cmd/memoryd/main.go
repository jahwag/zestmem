package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/buildinfo"
	"github.com/bytelope/zestmem/internal/console"
	"github.com/bytelope/zestmem/internal/embed"
	"github.com/bytelope/zestmem/internal/httpx"
	"github.com/bytelope/zestmem/internal/mcpserver"
	"github.com/bytelope/zestmem/internal/memory"
	"github.com/bytelope/zestmem/internal/postgres"
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
		authenticator, err = authn.New(ctx, issuer, audience, store)
		if err != nil {
			slog.Error("OIDC discovery", "error", err)
			os.Exit(1)
		}
	} else {
		authenticator = authn.NewAPIKeyOnly(store)
	}
	metrics := &httpx.Metrics{}
	domain := memory.NewService(store, store)
	tei := embed.NewTEI(required("TEI_URL"), 15*time.Second)
	domain.WithEmbedder(embed.NewObserved(embed.NewLimited(tei, envInt("TEI_CONCURRENCY", 4)), metrics))
	mcpHandler := mcpserver.New(domain, metrics).Handler()
	limiter := httpx.NewLimiter(envInt("RATE_LIMIT_PER_MINUTE", 120), envInt("RATE_LIMIT_BURST", 20), metrics)
	protect := func(next http.Handler) http.Handler { return authenticator.Middleware(limiter.Middleware(next)) }
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(limiter.Middleware(http.MaxBytesHandler(mcpHandler, 1<<20))))
	consoleHandler := console.Handler(domain, protect)
	mux.Handle("/console/", consoleHandler)
	mux.Handle("/api/v1/", consoleHandler)
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
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.Handle("/bootstrap/catalog", authenticator.Middleware(limiter.Middleware(bootstrapHandler)))
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
