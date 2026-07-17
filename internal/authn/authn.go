package authn

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"

	"github.com/bytelope/zestmem/internal/memory"
	"github.com/coreos/go-oidc/v3/oidc"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type PrincipalLookup interface {
	ByExternalIdentity(context.Context, string, string) (memory.Principal, error)
	ByAPIKeyDigest(context.Context, []byte) (memory.Principal, error)
}

type Authenticator struct {
	issuer   string
	verifier *oidc.IDTokenVerifier
	lookup   PrincipalLookup
}

func New(ctx context.Context, issuer, audience string, lookup PrincipalLookup) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		issuer:   issuer,
		verifier: provider.Verifier(&oidc.Config{ClientID: audience}),
		lookup:   lookup,
	}, nil
}

func NewAPIKeyOnly(lookup PrincipalLookup) *Authenticator {
	return &Authenticator{lookup: lookup}
}

func (a *Authenticator) Authenticate(ctx context.Context, token string) (memory.Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return memory.Principal{}, ErrUnauthenticated
	}
	if isAPIKey(token) {
		digest := sha256.Sum256([]byte(token))
		return a.lookup.ByAPIKeyDigest(ctx, digest[:])
	}
	if a.verifier != nil {
		idToken, err := a.verifier.Verify(ctx, token)
		if err != nil {
			return memory.Principal{}, ErrUnauthenticated
		}
		return a.lookup.ByExternalIdentity(ctx, a.issuer, idToken.Subject)
	}
	return memory.Principal{}, ErrUnauthenticated
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, principal memory.Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (memory.Principal, bool) {
	principal, ok := ctx.Value(contextKey{}).(memory.Principal)
	return principal, ok
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-memory"`)
			http.Error(w, "missing bearer credential", http.StatusUnauthorized)
			return
		}
		principal, err := a.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-memory", error="invalid_token"`)
			http.Error(w, "invalid bearer credential", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}
