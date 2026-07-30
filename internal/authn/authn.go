package authn

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

var (
	ErrUnauthenticated     = errors.New("unauthenticated")
	ErrVerifierUnavailable = errors.New("authentication verifier unavailable")
)

type PrincipalLookup interface {
	ByExternalIdentity(context.Context, string, string) (memory.Principal, error)
	ByAPIKeyDigest(context.Context, []byte) (memory.Principal, error)
}

type expiringAPIKeyLookup interface {
	ByAPIKeyDigestWithExpiry(context.Context, []byte) (memory.Principal, time.Time, error)
}

type Authenticator struct {
	issuer       string
	subjectClaim string
	requiredRole string
	verifier     *oidc.IDTokenVerifier
	lookup       PrincipalLookup
	transport    *cooldownTransport
}

func New(ctx context.Context, issuer, audience string, lookup PrincipalLookup) (*Authenticator, error) {
	return NewWithOptions(ctx, issuer, audience, "sub", "", lookup)
}

func NewWithOptions(ctx context.Context, issuer, audience, subjectClaim, requiredRole string, lookup PrincipalLookup) (*Authenticator, error) {
	subjectClaim, err := validateSubjectClaim(subjectClaim)
	if err != nil {
		return nil, err
	}
	ctx, transport := newOIDCClientContext(ctx)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		issuer:       issuer,
		subjectClaim: subjectClaim,
		requiredRole: requiredRole,
		verifier:     provider.Verifier(&oidc.Config{ClientID: audience}),
		lookup:       lookup,
		transport:    transport,
	}, nil
}

func NewWithRemoteKeySet(ctx context.Context, issuer, audience, jwksURL, subjectClaim, requiredRole string, lookup PrincipalLookup) (*Authenticator, error) {
	subjectClaim, err := validateSubjectClaim(subjectClaim)
	if err != nil {
		return nil, err
	}
	ctx, transport := newOIDCClientContext(ctx)
	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
	return &Authenticator{
		issuer:       issuer,
		subjectClaim: subjectClaim,
		requiredRole: requiredRole,
		verifier:     oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: audience}),
		lookup:       lookup,
		transport:    transport,
	}, nil
}

func NewAPIKeyOnly(lookup PrincipalLookup) *Authenticator {
	return &Authenticator{lookup: lookup}
}

func (a *Authenticator) Authenticate(ctx context.Context, token string) (memory.Principal, error) {
	credential, err := a.authenticate(ctx, token)
	return credential.principal, err
}

type credential struct {
	principal    memory.Principal
	expiry       time.Time
	scopes       []string
	apiKeyDigest []byte
}

func (a *Authenticator) authenticate(ctx context.Context, token string) (credential, error) {
	return a.authenticateWithNonce(ctx, token, "")
}

func (a *Authenticator) authenticateWithNonce(ctx context.Context, token, expectedNonce string) (credential, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return credential{}, ErrUnauthenticated
	}
	if isAPIKey(token) {
		if expectedNonce != "" {
			return credential{}, ErrUnauthenticated
		}
		digest := sha256.Sum256([]byte(token))
		return a.authenticateAPIKeyDigest(ctx, digest[:])
	}
	if a.verifier != nil {
		failuresBefore := uint64(0)
		if a.transport != nil {
			failuresBefore = a.transport.failureCount()
		}
		idToken, err := a.verifier.Verify(ctx, token)
		if err != nil {
			if errors.Is(err, ErrVerifierUnavailable) ||
				(a.transport != nil && a.transport.failureCount() > failuresBefore) {
				return credential{}, ErrVerifierUnavailable
			}
			return credential{}, ErrUnauthenticated
		}
		var claims struct {
			OID   string   `json:"oid"`
			Scope string   `json:"scp"`
			Roles []string `json:"roles"`
			Nonce string   `json:"nonce"`
		}
		if err := idToken.Claims(&claims); err != nil {
			return credential{}, ErrUnauthenticated
		}
		subject := idToken.Subject
		if a.subjectClaim == "oid" {
			subject = claims.OID
		}
		if subject == "" ||
			(a.requiredRole != "" && !contains(claims.Roles, a.requiredRole)) ||
			(expectedNonce != "" && !constantTimeEqual(claims.Nonce, expectedNonce)) {
			return credential{}, ErrUnauthenticated
		}
		principal, err := a.lookup.ByExternalIdentity(ctx, a.issuer, subject)
		if err != nil {
			return credential{}, err
		}
		scopes := append(strings.Fields(claims.Scope), claims.Roles...)
		return credential{principal: principal, expiry: idToken.Expiry, scopes: scopes}, nil
	}
	return credential{}, ErrUnauthenticated
}

func (a *Authenticator) authenticateAPIKeyDigest(ctx context.Context, digest []byte) (credential, error) {
	var principal memory.Principal
	var expiry time.Time
	var err error
	if lookup, ok := a.lookup.(expiringAPIKeyLookup); ok {
		principal, expiry, err = lookup.ByAPIKeyDigestWithExpiry(ctx, digest)
	} else {
		principal, err = a.lookup.ByAPIKeyDigest(ctx, digest)
	}
	if err != nil {
		return credential{}, err
	}
	return credential{
		principal:    principal,
		expiry:       expiry,
		apiKeyDigest: append([]byte(nil), digest...),
	}, nil
}

func constantTimeEqual(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateSubjectClaim(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "sub", nil
	}
	if value != "sub" && value != "oid" {
		return "", errors.New("OIDC subject claim must be sub or oid")
	}
	return value, nil
}

// TokenVerifier adapts ZestMem's native API keys and OIDC workload identities
// to the official MCP SDK's per-request resource-server authentication.
func (a *Authenticator) TokenVerifier(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	credential, err := a.authenticate(ctx, token)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) || errors.Is(err, memory.ErrNotFound) {
			return nil, errors.Join(auth.ErrInvalidToken, err)
		}
		return nil, errors.Join(ErrVerifierUnavailable, err)
	}
	return &auth.TokenInfo{
		Scopes:     credential.scopes,
		Expiration: credential.expiry,
		UserID:     credential.principal.ID,
		Extra: map[string]any{
			"principal": credential.principal,
		},
	}, nil
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
