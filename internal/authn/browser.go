package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jahwag/zestmem/internal/memory"
)

const ConsoleSessionCookie = "zestmem_console_session"

const (
	consoleSessionTTL = 8 * time.Hour
	maxSessions       = 256
)

type browserSession struct {
	principal    memory.Principal
	expiresAt    time.Time
	apiKeyDigest []byte
}

type BrowserSessions struct {
	mu              sync.Mutex
	sessions        map[[sha256.Size]byte]browserSession
	native          *Authenticator
	assertion       *Authenticator
	assertionHeader string
	publicOrigin    string
	publicHost      string
	secureCookie    bool
	oidc            *BrowserOIDC
	now             func() time.Time
}

func NewBrowserSessions(native, assertion *Authenticator, assertionHeader, publicOrigin string) (*BrowserSessions, error) {
	publicOrigin = strings.TrimSuffix(publicOrigin, "/")
	secure := false
	publicHost := ""
	if publicOrigin != "" {
		parsed, err := url.Parse(publicOrigin)
		if err != nil || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			parsed.Path != "" {
			return nil, errors.New("invalid console public origin")
		}
		secure = parsed.Scheme == "https"
		publicHost = parsed.Host
	}
	if assertion != nil && assertionHeader == "" {
		return nil, errors.New("assertion header is required when assertion authentication is configured")
	}
	return &BrowserSessions{
		sessions:        make(map[[sha256.Size]byte]browserSession),
		native:          native,
		assertion:       assertion,
		assertionHeader: assertionHeader,
		publicOrigin:    publicOrigin,
		publicHost:      publicHost,
		secureCookie:    secure,
		now:             time.Now,
	}, nil
}

func (s *BrowserSessions) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.availableHost(r) {
			http.NotFound(w, r)
			return
		}
		if session, ok := s.session(r); ok {
			if len(session.apiKeyDigest) > 0 {
				credential, err := s.native.authenticateAPIKeyDigest(
					r.Context(),
					session.apiKeyDigest,
				)
				if err != nil || credential.principal.ID != session.principal.ID {
					s.revokeRequestSession(r)
					s.clearCookie(w)
				} else {
					next.ServeHTTP(
						w,
						r.WithContext(WithPrincipal(r.Context(), credential.principal)),
					)
					return
				}
			} else {
				next.ServeHTTP(
					w,
					r.WithContext(WithPrincipal(r.Context(), session.principal)),
				)
				return
			}
		}
		if s.assertion != nil {
			if raw := strings.TrimSpace(r.Header.Get(s.assertionHeader)); raw != "" {
				credential, err := s.assertion.authenticate(r.Context(), raw)
				if err == nil {
					if _, err := s.start(w, credential); err == nil {
						next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), credential.principal)))
						return
					}
				}
			}
		}
		w.Header().Set("WWW-Authenticate", `Session realm="zestmem-console"`)
		http.Error(w, "console session required", http.StatusUnauthorized)
	})
}

func (s *BrowserSessions) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if !s.availableHost(r) {
		http.NotFound(w, r)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "cross-origin login refused", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.Token) == "" {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	credential, err := s.native.authenticate(r.Context(), input.Token)
	if err != nil {
		http.Error(w, "invalid credential", http.StatusUnauthorized)
		return
	}
	expiresAt, err := s.start(w, credential)
	if err != nil {
		http.Error(w, "too many active console sessions", http.StatusTooManyRequests)
		return
	}
	writeBrowserJSON(w, http.StatusOK, map[string]string{"expires_at": expiresAt.Format(time.RFC3339)})
}

func (s *BrowserSessions) SessionHandler(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "console session required", http.StatusUnauthorized)
		return
	}
	writeBrowserJSON(w, http.StatusOK, map[string]string{
		"principal_id": principal.ID,
		"type":         principal.Type,
		"display_name": principal.DisplayName,
	})
}

func (s *BrowserSessions) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if !s.availableHost(r) {
		http.NotFound(w, r)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "cross-origin logout refused", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(ConsoleSessionCookie); err == nil {
		digest := sha256.Sum256([]byte(cookie.Value))
		s.mu.Lock()
		delete(s.sessions, digest)
		s.mu.Unlock()
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *BrowserSessions) start(
	w http.ResponseWriter,
	credential credential,
) (time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	now := s.now()
	expiresAt := now.Add(consoleSessionTTL)
	if !credential.expiry.IsZero() && credential.expiry.Before(expiresAt) {
		expiresAt = credential.expiry
	}
	if !expiresAt.After(now) {
		return time.Time{}, errors.New("credential already expired")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	if len(s.sessions) >= maxSessions {
		return time.Time{}, errors.New("console session capacity reached")
	}
	s.sessions[digest] = browserSession{
		principal:    credential.principal,
		expiresAt:    expiresAt,
		apiKeyDigest: append([]byte(nil), credential.apiKeyDigest...),
	}
	http.SetCookie(w, &http.Cookie{
		Name: ConsoleSessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, Expires: expiresAt,
	})
	return expiresAt, nil
}

func (s *BrowserSessions) session(r *http.Request) (browserSession, bool) {
	cookie, err := r.Cookie(ConsoleSessionCookie)
	if err != nil || cookie.Value == "" {
		return browserSession{}, false
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	session, ok := s.sessions[digest]
	return session, ok
}

func (s *BrowserSessions) revokeRequestSession(r *http.Request) {
	cookie, err := r.Cookie(ConsoleSessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	delete(s.sessions, digest)
	s.mu.Unlock()
}

func (s *BrowserSessions) cleanupLocked(now time.Time) {
	for digest, session := range s.sessions {
		if !session.expiresAt.After(now) {
			delete(s.sessions, digest)
		}
	}
}

func (s *BrowserSessions) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ConsoleSessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

func (s *BrowserSessions) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	if s.publicOrigin != "" {
		return origin == s.publicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}

func (s *BrowserSessions) availableHost(r *http.Request) bool {
	return s.publicHost == "" || r.Host == s.publicHost
}

func writeBrowserJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
