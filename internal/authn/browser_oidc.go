package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const ConsoleOIDCFlowCookie = "zestmem_console_oidc"

const (
	consoleOIDCFlowTTL = 10 * time.Minute
	maxOIDCFlows       = 64
)

type browserOIDCFlow struct {
	verifier string
	nonce    string
	expires  time.Time
}

// BrowserOIDC implements a standard Authorization Code + PKCE browser login.
// It is optional and independent of edge-assertion or API-key session exchange.
type BrowserOIDC struct {
	authenticator *Authenticator
	config        oauth2.Config
	mu            sync.Mutex
	flows         map[[sha256.Size]byte]browserOIDCFlow
	now           func() time.Time
	random        io.Reader
}

func NewBrowserOIDC(
	ctx context.Context,
	issuer string,
	clientID string,
	clientSecret string,
	redirectURL string,
	scopes []string,
	subjectClaim string,
	requiredRole string,
	lookup PrincipalLookup,
) (*BrowserOIDC, error) {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	redirectURL = strings.TrimSpace(redirectURL)
	if issuer == "" || clientID == "" || redirectURL == "" {
		return nil, errors.New("browser OIDC issuer, client ID, and redirect URL are required")
	}
	subjectClaim, err := validateSubjectClaim(subjectClaim)
	if err != nil {
		return nil, err
	}
	ctx, transport := newOIDCClientContext(ctx)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	} else if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &BrowserOIDC{
		authenticator: &Authenticator{
			issuer:       issuer,
			subjectClaim: subjectClaim,
			requiredRole: requiredRole,
			verifier:     provider.Verifier(&oidc.Config{ClientID: clientID}),
			lookup:       lookup,
			transport:    transport,
		},
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret,
			Endpoint: provider.Endpoint(), RedirectURL: redirectURL, Scopes: scopes,
		},
		flows:  make(map[[sha256.Size]byte]browserOIDCFlow),
		now:    time.Now,
		random: rand.Reader,
	}, nil
}

func (s *BrowserSessions) WithOIDC(provider *BrowserOIDC) *BrowserSessions {
	s.oidc = provider
	return s
}

func (s *BrowserSessions) AuthConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !s.availableHost(r) {
		http.NotFound(w, r)
		return
	}
	writeBrowserJSON(w, http.StatusOK, map[string]bool{"oidc": s.oidc != nil})
}

func (s *BrowserSessions) OIDCStartHandler(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || !s.availableHost(r) {
		http.NotFound(w, r)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "cross-origin login refused", http.StatusForbidden)
		return
	}
	previousState := ""
	if cookie, err := r.Cookie(ConsoleOIDCFlowCookie); err == nil {
		previousState = cookie.Value
	}
	state, verifier, nonce, err := s.oidc.newFlow(previousState)
	if err != nil {
		http.Error(w, "OIDC login capacity reached", http.StatusTooManyRequests)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ConsoleOIDCFlowCookie, Value: state,
		Path: "/console/auth/oidc", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(consoleOIDCFlowTTL.Seconds()),
	})
	location := s.oidc.config.AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
	http.Redirect(w, r, location, http.StatusFound)
}

func (s *BrowserSessions) OIDCCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || !s.availableHost(r) {
		http.NotFound(w, r)
		return
	}
	stateValues, codeValues := r.URL.Query()["state"], r.URL.Query()["code"]
	if len(stateValues) != 1 {
		s.clearOIDCFlowCookie(w)
		http.Error(w, "invalid OIDC callback", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(ConsoleOIDCFlowCookie)
	if err != nil || !constantTimeEqual(cookie.Value, stateValues[0]) {
		s.clearOIDCFlowCookie(w)
		http.Error(w, "invalid OIDC login state", http.StatusUnauthorized)
		return
	}
	flow, ok := s.oidc.consumeFlow(stateValues[0])
	s.clearOIDCFlowCookie(w)
	if !ok {
		http.Error(w, "expired OIDC login state", http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("error") != "" || len(codeValues) != 1 {
		http.Error(w, "identity provider refused login", http.StatusUnauthorized)
		return
	}
	token, err := s.oidc.config.Exchange(
		r.Context(),
		codeValues[0],
		oauth2.VerifierOption(flow.verifier),
	)
	if err != nil {
		http.Error(w, "OIDC code exchange failed", http.StatusUnauthorized)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "identity provider omitted ID token", http.StatusUnauthorized)
		return
	}
	credential, err := s.oidc.authenticator.authenticateWithNonce(
		r.Context(),
		rawIDToken,
		flow.nonce,
	)
	if err != nil {
		http.Error(w, "invalid OIDC identity", http.StatusUnauthorized)
		return
	}
	if _, err := s.start(w, credential); err != nil {
		http.Error(w, "too many active console sessions", http.StatusTooManyRequests)
		return
	}
	http.Redirect(w, r, "/console/", http.StatusSeeOther)
}

func (o *BrowserOIDC) newFlow(previousState string) (state, verifier, nonce string, err error) {
	state, err = randomBrowserValue(o.random, 32)
	if err != nil {
		return "", "", "", err
	}
	nonce, err = randomBrowserValue(o.random, 32)
	if err != nil {
		return "", "", "", err
	}
	verifier = oauth2.GenerateVerifier()
	digest := sha256.Sum256([]byte(state))
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanupLocked(now)
	if previousState != "" {
		delete(o.flows, sha256.Sum256([]byte(previousState)))
	}
	if len(o.flows) >= maxOIDCFlows {
		var oldestDigest [sha256.Size]byte
		var oldestExpiry time.Time
		for existingDigest, flow := range o.flows {
			if oldestExpiry.IsZero() || flow.expires.Before(oldestExpiry) {
				oldestDigest = existingDigest
				oldestExpiry = flow.expires
			}
		}
		delete(o.flows, oldestDigest)
	}
	o.flows[digest] = browserOIDCFlow{
		verifier: verifier,
		nonce:    nonce,
		expires:  now.Add(consoleOIDCFlowTTL),
	}
	return state, verifier, nonce, nil
}

func (o *BrowserOIDC) consumeFlow(state string) (browserOIDCFlow, bool) {
	digest := sha256.Sum256([]byte(state))
	now := o.now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanupLocked(now)
	flow, ok := o.flows[digest]
	delete(o.flows, digest)
	return flow, ok && flow.expires.After(now)
}

func (o *BrowserOIDC) cleanupLocked(now time.Time) {
	for digest, flow := range o.flows {
		if !flow.expires.After(now) {
			delete(o.flows, digest)
		}
	}
}

func (s *BrowserSessions) clearOIDCFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ConsoleOIDCFlowCookie, Value: "",
		Path: "/console/auth/oidc", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func randomBrowserValue(random io.Reader, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
