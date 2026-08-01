package authn

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	ConsoleOIDCFlowCookie       = "zestmem_console_oidc"
	secureConsoleOIDCFlowCookie = "__Host-zestmem_console_oidc"
)

const (
	consoleOIDCFlowTTL        = 10 * time.Minute
	maxOIDCReplayMarkers      = 1024
	maxOIDCExchanges          = 8
	browserOIDCDiscoveryRetry = 30 * time.Second
)

type browserOIDCFlow struct {
	verifier string
	nonce    string
	expires  time.Time
}

type browserOIDCFlowPayload struct {
	Verifier    string `json:"v"`
	Nonce       string `json:"n"`
	ExpiresUnix int64  `json:"e"`
}

// BrowserOIDC implements a standard Authorization Code + PKCE browser login.
// It is optional and independent of edge-assertion or API-key session exchange.
type BrowserOIDC struct {
	authenticator *Authenticator
	issuer        string
	clientID      string
	client        *http.Client
	config        oauth2.Config
	providerMu    sync.Mutex
	providerErr   error
	providerRetry time.Time
	mu            sync.Mutex
	flowAEAD      cipher.AEAD
	usedFlows     map[[sha256.Size]byte]time.Time
	exchangeSlots chan struct{}
	now           func() time.Time
	random        io.Reader
}

func NewBrowserOIDC(
	_ context.Context,
	issuer string,
	clientID string,
	clientSecret string,
	redirectURL string,
	scopes []string,
	subjectClaim string,
	requiredRole string,
	lookup PrincipalLookup,
) (*BrowserOIDC, error) {
	issuer = strings.TrimSpace(issuer)
	clientID = strings.TrimSpace(clientID)
	redirectURL = strings.TrimSpace(redirectURL)
	if issuer == "" || clientID == "" || redirectURL == "" {
		return nil, errors.New("browser OIDC issuer, client ID, and redirect URL are required")
	}
	if err := validateBrowserOIDCURL(issuer, "issuer"); err != nil {
		return nil, err
	}
	if err := validateBrowserOIDCURL(redirectURL, "redirect URL"); err != nil {
		return nil, err
	}
	subjectClaim, err := validateSubjectClaim(subjectClaim)
	if err != nil {
		return nil, err
	}
	client, transport := newOIDCClient()
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	} else if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &BrowserOIDC{
		authenticator: &Authenticator{
			issuer:                  issuer,
			audience:                clientID,
			subjectClaim:            subjectClaim,
			requiredRole:            requiredRole,
			validateAuthorizedParty: true,
			lookup:                  lookup,
			transport:               transport,
		},
		issuer:   issuer,
		clientID: clientID,
		client:   client,
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret,
			RedirectURL: redirectURL, Scopes: scopes,
		},
		usedFlows:     make(map[[sha256.Size]byte]time.Time),
		exchangeSlots: make(chan struct{}, maxOIDCExchanges),
		now:           time.Now,
		random:        rand.Reader,
	}, nil
}

func validateBrowserOIDCURL(raw, label string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("browser OIDC %s must be an absolute HTTPS URL", label)
	}
	if parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackBrowserHost(parsed.Host)) {
		return nil
	}
	return fmt.Errorf("browser OIDC %s must use HTTPS except on loopback", label)
}

func (o *BrowserOIDC) ensureProvider(_ context.Context) error {
	o.providerMu.Lock()
	defer o.providerMu.Unlock()
	if o.authenticator.verifier != nil {
		return nil
	}
	now := o.now()
	if o.providerErr != nil && now.Before(o.providerRetry) {
		return o.providerErr
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(context.Background(), o.client), o.issuer)
	if err == nil {
		endpoint := provider.Endpoint()
		var metadata struct {
			JWKSURI string `json:"jwks_uri"`
		}
		err = provider.Claims(&metadata)
		if err == nil {
			for label, raw := range map[string]string{
				"authorization endpoint": endpoint.AuthURL,
				"token endpoint":         endpoint.TokenURL,
				"JWKS endpoint":          metadata.JWKSURI,
			} {
				if err = validateBrowserOIDCURL(raw, label); err != nil {
					break
				}
			}
		}
		if err == nil {
			o.authenticator.verifier = provider.Verifier(&oidc.Config{ClientID: o.clientID})
			o.config.Endpoint = endpoint
			o.providerErr = nil
			o.providerRetry = time.Time{}
			return nil
		}
	}
	o.providerErr = err
	o.providerRetry = now.Add(browserOIDCDiscoveryRetry)
	return err
}

func (s *BrowserSessions) WithOIDC(provider *BrowserOIDC) *BrowserSessions {
	s.oidc = provider
	if s.lookup == nil && provider != nil && provider.authenticator != nil {
		s.lookup = provider.authenticator.lookup
	}
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
	if err := s.oidc.ensureProvider(r.Context()); err != nil {
		http.Error(w, "OIDC login unavailable", http.StatusServiceUnavailable)
		return
	}
	previousState := ""
	if cookie, err := r.Cookie(s.oidcFlowCookieName()); err == nil {
		previousState = cookie.Value
	}
	state, verifier, nonce, err := s.oidc.newFlow(previousState)
	if err != nil {
		http.Error(w, "OIDC login capacity reached", http.StatusTooManyRequests)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.oidcFlowCookieName(), Value: state,
		Path: s.oidcFlowCookiePath(), HttpOnly: true,
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
	cookie, err := r.Cookie(s.oidcFlowCookieName())
	if err != nil || !constantTimeEqual(cookie.Value, stateValues[0]) {
		s.clearOIDCFlowCookie(w)
		http.Error(w, "invalid OIDC login state", http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("error") == "" && len(codeValues) == 1 {
		releaseExchange, admitted := s.oidc.beginExchange()
		if !admitted {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many concurrent OIDC logins", http.StatusTooManyRequests)
			return
		}
		defer releaseExchange()
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
	exchangeContext := r.Context()
	if s.oidc.client != nil {
		exchangeContext = oidc.ClientContext(exchangeContext, s.oidc.client)
	}
	token, err := s.oidc.config.Exchange(
		exchangeContext,
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
	if _, err := s.start(w, credential, s.requestSessionToken(r)); err != nil {
		http.Error(w, "too many active console sessions", http.StatusTooManyRequests)
		return
	}
	http.Redirect(w, r, "/console/", http.StatusSeeOther)
}

func (o *BrowserOIDC) beginExchange() (func(), bool) {
	if o.exchangeSlots == nil {
		return func() {}, true
	}
	select {
	case o.exchangeSlots <- struct{}{}:
		return func() { <-o.exchangeSlots }, true
	default:
		return nil, false
	}
}

func (o *BrowserOIDC) ensureFlowAEAD() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.flowAEAD != nil {
		return nil
	}
	if o.random == nil {
		o.random = rand.Reader
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(o.random, key); err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	flowAEAD, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	o.flowAEAD = flowAEAD
	if o.usedFlows == nil {
		o.usedFlows = make(map[[sha256.Size]byte]time.Time)
	}
	return nil
}

func (o *BrowserOIDC) newFlow(previousState string) (state, verifier, nonce string, err error) {
	_ = previousState
	nonce, err = randomBrowserValue(o.random, 32)
	if err != nil {
		return "", "", "", err
	}
	verifier = oauth2.GenerateVerifier()
	now := o.now()
	payload, err := json.Marshal(browserOIDCFlowPayload{
		Verifier:    verifier,
		Nonce:       nonce,
		ExpiresUnix: now.Add(consoleOIDCFlowTTL).Unix(),
	})
	if err != nil {
		return "", "", "", err
	}
	if err := o.ensureFlowAEAD(); err != nil {
		return "", "", "", err
	}
	sealNonce := make([]byte, o.flowAEAD.NonceSize())
	if _, err := io.ReadFull(o.random, sealNonce); err != nil {
		return "", "", "", err
	}
	state = base64.RawURLEncoding.EncodeToString(o.flowAEAD.Seal(sealNonce, sealNonce, payload, nil))
	return state, verifier, nonce, nil
}

func (o *BrowserOIDC) consumeFlow(state string) (browserOIDCFlow, bool) {
	var flow browserOIDCFlow
	if err := o.ensureFlowAEAD(); err != nil {
		return flow, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(state)
	if err != nil || len(raw) <= o.flowAEAD.NonceSize() || len(raw) > 2048 {
		return flow, false
	}
	plaintext, err := o.flowAEAD.Open(nil, raw[:o.flowAEAD.NonceSize()], raw[o.flowAEAD.NonceSize():], nil)
	if err != nil {
		return flow, false
	}
	var payload browserOIDCFlowPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return flow, false
	}
	now := o.now()
	flow = browserOIDCFlow{
		verifier: payload.Verifier,
		nonce:    payload.Nonce,
		expires:  time.Unix(payload.ExpiresUnix, 0),
	}
	if flow.verifier == "" || flow.nonce == "" || !flow.expires.After(now) {
		return browserOIDCFlow{}, false
	}
	digest := sha256.Sum256(raw)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanupLocked(now)
	if _, used := o.usedFlows[digest]; used {
		return browserOIDCFlow{}, false
	}
	if len(o.usedFlows) >= maxOIDCReplayMarkers {
		var oldestDigest [sha256.Size]byte
		var oldestExpiry time.Time
		for existingDigest, expiry := range o.usedFlows {
			if oldestExpiry.IsZero() || expiry.Before(oldestExpiry) {
				oldestDigest = existingDigest
				oldestExpiry = expiry
			}
		}
		delete(o.usedFlows, oldestDigest)
	}
	o.usedFlows[digest] = flow.expires
	return flow, true
}

func (o *BrowserOIDC) cleanupLocked(now time.Time) {
	for digest, expiry := range o.usedFlows {
		if !expiry.After(now) {
			delete(o.usedFlows, digest)
		}
	}
}

func (s *BrowserSessions) clearOIDCFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.oidcFlowCookieName(), Value: "",
		Path: s.oidcFlowCookiePath(), HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (s *BrowserSessions) oidcFlowCookieName() string {
	if s.secureCookie {
		return secureConsoleOIDCFlowCookie
	}
	return ConsoleOIDCFlowCookie
}

func (s *BrowserSessions) oidcFlowCookiePath() string {
	if s.secureCookie {
		return "/"
	}
	return "/console/auth/oidc"
}

func randomBrowserValue(random io.Reader, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
