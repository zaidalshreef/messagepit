package oidcauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/transport"
	"golang.org/x/oauth2"
)

const (
	stateCookie   = "messagepit_oidc_state"
	sessionCookie = "messagepit_oidc_session"
	flowLifetime  = 10 * time.Minute
	sessionLife   = 8 * time.Hour
	maxEntries    = 4096
)

type flow struct {
	nonce, verifier, returnTo string
	expires                   time.Time
}

type session struct {
	principal Principal
	expires   time.Time
	ctx       context.Context
	cancel    context.CancelFunc
}

type sessionKey struct{}
type principalKey struct{}
type serviceKey struct{}

// Principal identifies an authenticated user at a specific issuer.
type Principal struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Admin   bool   `json:"admin"`
	// LocalVersion invalidates local sessions after a password change.
	LocalVersion int64 `json:"-"`
}

// WithPrincipal associates a verified identity with a request context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// CurrentPrincipal returns the identity associated with a request, if present.
func CurrentPrincipal(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

// ServicePrincipal is an environment-scoped automated client.
type ServicePrincipal struct {
	ID            string
	EnvironmentID string
	Role          access.Role
}

// WithServicePrincipal associates a verified service token with a request.
func WithServicePrincipal(ctx context.Context, principal ServicePrincipal) context.Context {
	return context.WithValue(ctx, serviceKey{}, principal)
}

// CurrentServicePrincipal returns the authenticated service identity, if present.
func CurrentServicePrincipal(ctx context.Context) (ServicePrincipal, bool) {
	principal, ok := ctx.Value(serviceKey{}).(ServicePrincipal)
	return principal, ok
}

// Manager handles OIDC sign-in and short-lived in-memory browser sessions.
type Manager struct {
	config        Config
	oauth         oauth2.Config
	verifier      *oidc.IDTokenVerifier
	client        *http.Client
	allowed       map[string]struct{}
	admins        map[string]struct{}
	origin        *url.URL
	secure        bool
	validateToken func(context.Context, string) (ServicePrincipal, error)
	audit         func(Principal, string)
	localLogin    LocalLogin
	localValidate LocalValidate
	localAttempts map[string]*localAttempt
	loginSlots    chan struct{}

	mu       sync.Mutex
	flows    map[string]flow
	sessions map[[sha256.Size]byte]*session
}

// SetTokenValidator configures service-token authentication before serving requests.
func (m *Manager) SetTokenValidator(validate func(context.Context, string) (ServicePrincipal, error)) {
	m.validateToken = validate
}

// SetAuditHook configures optional session event recording before serving requests.
func (m *Manager) SetAuditHook(hook func(Principal, string)) {
	m.audit = hook
}

// New discovers the provider before the server starts, so failures cannot disable authentication.
func New(ctx context.Context, cfg Config) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, errors.New("OIDC is not configured")
	}
	origin, _ := secureURL(cfg.PublicURL, false)
	client := &http.Client{Timeout: 10 * time.Second}
	providerContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(providerContext, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	endpoint := provider.Endpoint()
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("read OIDC provider metadata: %w", err)
	}
	for name, address := range map[string]string{
		"authorization endpoint": endpoint.AuthURL,
		"token endpoint":         endpoint.TokenURL,
		"JWKS endpoint":          metadata.JWKSURL,
	} {
		if _, err := secureURL(address, true); err != nil {
			return nil, fmt.Errorf("OIDC %s: %w", name, err)
		}
	}

	return &Manager{
		config: cfg,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			Endpoint: endpoint, RedirectURL: strings.TrimRight(cfg.PublicURL, "/") + cfg.Webroot + "auth/callback",
			Scopes: []string{oidc.ScopeOpenID, "profile"},
		},
		verifier: provider.VerifierContext(oidc.ClientContext(context.Background(), client), &oidc.Config{ClientID: cfg.ClientID}),
		client:   client, allowed: cfg.subjects(), admins: parseSubjects(cfg.AdminSubjects), origin: origin, secure: origin.Scheme == "https",
		flows: make(map[string]flow), sessions: make(map[[sha256.Size]byte]*session),
	}, nil
}

// Wrap protects the UI and API, while leaving health and sign-in routes available.
func (m *Manager) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case m.config.Webroot + "auth/login":
			if m.localLogin != nil {
				m.handleLocalLogin(w, r)
			} else {
				m.login(w, r)
			}
			return
		case m.config.Webroot + "auth/callback":
			if m.localLogin != nil {
				http.NotFound(w, r)
				return
			}
			m.callback(w, r)
			return
		case m.config.Webroot + "auth/logout":
			m.logout(w, r)
			return
		case m.config.Webroot + "auth/signed-out":
			m.signedOut(w, r)
			return
		case m.config.Webroot + "livez", m.config.Webroot + "readyz":
			next.ServeHTTP(w, r)
			return
		}
		if value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if m.validateToken == nil || !transport.SecureOrLoopback(r) || !strings.HasPrefix(r.URL.Path, m.config.Webroot+"api/v1/environments/") {
				http.Error(w, "Invalid service token", http.StatusUnauthorized)
				return
			}
			principal, err := m.validateToken(r.Context(), value)
			if err != nil {
				http.Error(w, "Invalid service token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithServicePrincipal(r.Context(), principal)))
			return
		}

		s := m.currentSession(r)
		if s != nil && m.localValidate != nil && !m.localValidate(r.Context(), s.principal) {
			m.deleteSession(r)
			m.cookie(w, sessionCookie, "", -1, http.SameSiteStrictMode)
			s = nil
		}
		if s == nil {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") && !strings.HasPrefix(r.URL.Path, m.config.Webroot+"api/") {
				target := m.config.Webroot + "auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		if (!safeMethod(r.Method) || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) && !m.sameOrigin(r) {
			http.Error(w, "Invalid origin", http.StatusForbidden)
			return
		}
		if !s.principal.Admin && !m.memberPath(r.URL.Path) {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") && !strings.HasPrefix(r.URL.Path, m.config.Webroot+"api/") {
				http.Redirect(w, r, m.config.Webroot+"groups", http.StatusFound)
				return
			}
			http.Error(w, "Access to the shared mailbox is restricted", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, s)
		next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, s.principal)))
	})
}

func (m *Manager) memberPath(path string) bool {
	root := m.config.Webroot
	if path == root+"groups" || path == root+"projects" || path == root+"api/v1/me" || path == root+"api/v1/local-password" || strings.HasPrefix(path, root+"dist/") {
		return true
	}
	for _, name := range []string{"favicon.ico", "favicon.svg", "messagepit.svg", "notification.png"} {
		if path == root+name {
			return true
		}
	}
	for _, base := range []string{root + "api/v1/groups", root + "api/v1/projects", root + "api/v1/environments"} {
		if path == base || strings.HasPrefix(path, base+"/") {
			return true
		}
	}
	return false
}

// Lifetime returns the authenticated session's cancellation context, if any.
func Lifetime(ctx context.Context) context.Context {
	if s, ok := ctx.Value(sessionKey{}).(*session); ok {
		return s.ctx
	}
	return nil
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, err := randomToken()
	if err != nil {
		http.Error(w, "Unable to start sign-in", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		http.Error(w, "Unable to start sign-in", http.StatusInternalServerError)
		return
	}
	verifier, err := randomToken()
	if err != nil {
		http.Error(w, "Unable to start sign-in", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	m.mu.Lock()
	m.prune(now)
	if len(m.flows) >= maxEntries {
		m.mu.Unlock()
		http.Error(w, "Too many sign-in attempts", http.StatusServiceUnavailable)
		return
	}
	m.flows[state] = flow{nonce: nonce, verifier: verifier, returnTo: m.safeReturnTo(r.URL.Query().Get("return_to")), expires: now.Add(flowLifetime)}
	m.mu.Unlock()
	m.cookie(w, stateCookie, state, int(flowLifetime.Seconds()), http.SameSiteLaxMode)
	http.Redirect(w, r, m.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (m *Manager) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m.cookie(w, stateCookie, "", -1, http.SameSiteLaxMode)
	states, codes := r.URL.Query()["state"], r.URL.Query()["code"]
	cookie, err := r.Cookie(stateCookie)
	if err != nil || len(states) != 1 || len(codes) != 1 || !hmac.Equal([]byte(cookie.Value), []byte(states[0])) {
		http.Error(w, "Invalid sign-in response", http.StatusUnauthorized)
		return
	}
	m.mu.Lock()
	f, found := m.flows[states[0]]
	delete(m.flows, states[0])
	m.mu.Unlock()
	if !found || !time.Now().Before(f.expires) {
		http.Error(w, "Sign-in expired", http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), m.client), 10*time.Second)
	defer cancel()
	token, err := m.oauth.Exchange(ctx, codes[0], oauth2.VerifierOption(f.verifier))
	if err != nil {
		http.Error(w, "Unable to complete sign-in", http.StatusUnauthorized)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "Invalid identity token", http.StatusUnauthorized)
		return
	}
	id, err := m.verifier.Verify(ctx, raw)
	if err != nil || id.Subject == "" || !hmac.Equal([]byte(id.Nonce), []byte(f.nonce)) {
		http.Error(w, "Invalid identity token", http.StatusUnauthorized)
		return
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if err := id.Claims(&claims); err != nil || (len(id.Audience) > 1 && claims.AuthorizedParty == "") || (claims.AuthorizedParty != "" && claims.AuthorizedParty != m.config.ClientID) {
		http.Error(w, "Invalid identity token", http.StatusUnauthorized)
		return
	}
	if _, allowed := m.allowed[id.Subject]; !allowed {
		http.Error(w, "Account is not authorized", http.StatusForbidden)
		return
	}
	now := time.Now()
	expires := now.Add(sessionLife)
	if id.Expiry.Before(expires) {
		expires = id.Expiry
	}
	_, admin := m.admins[id.Subject]
	principal := Principal{Issuer: m.config.Issuer, Subject: id.Subject, Admin: admin}
	m.startSession(w, r, principal, expires, f.returnTo)
}

func (m *Manager) startSession(w http.ResponseWriter, r *http.Request, principal Principal, expires time.Time, returnTo string) {
	value, err := randomToken()
	if err != nil {
		http.Error(w, "Unable to create session", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	lifetime, stop := context.WithDeadline(context.Background(), expires)
	m.mu.Lock()
	m.prune(now)
	if len(m.sessions) >= maxEntries {
		m.mu.Unlock()
		stop()
		http.Error(w, "Too many active sessions", http.StatusServiceUnavailable)
		return
	}
	m.sessions[sha256.Sum256([]byte(value))] = &session{
		principal: principal,
		expires:   expires, ctx: lifetime, cancel: stop,
	}
	m.mu.Unlock()
	if m.audit != nil {
		m.audit(principal, "session.login")
	}
	age := max(1, int(time.Until(expires).Seconds()))
	m.cookie(w, sessionCookie, value, age, http.SameSiteStrictMode)
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

func (m *Manager) deleteSession(r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		key := sha256.Sum256([]byte(cookie.Value))
		m.mu.Lock()
		if s := m.sessions[key]; s != nil {
			s.cancel()
			delete(m.sessions, key)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !m.sameOrigin(r) {
		http.Error(w, "Invalid origin", http.StatusForbidden)
		return
	}
	var principal *Principal
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		key := sha256.Sum256([]byte(cookie.Value))
		m.mu.Lock()
		if s := m.sessions[key]; s != nil {
			p := s.principal
			principal = &p
			s.cancel()
			delete(m.sessions, key)
		}
		m.mu.Unlock()
	}
	if principal != nil && m.audit != nil {
		m.audit(*principal, "session.logout")
	}
	m.cookie(w, sessionCookie, "", -1, http.SameSiteStrictMode)
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) signedOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	_, _ = fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><title>Signed out</title><body><h1>Signed out</h1><a href=\"%s\">Sign in</a></body></html>", html.EscapeString(m.config.Webroot+"auth/login"))
}

func (m *Manager) currentSession(r *http.Request) *session {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	key := sha256.Sum256([]byte(cookie.Value))
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s != nil && !time.Now().Before(s.expires) {
		s.cancel()
		delete(m.sessions, key)
		return nil
	}
	return s
}

// prune must be called with m.mu held.
func (m *Manager) prune(now time.Time) {
	for key, f := range m.flows {
		if !now.Before(f.expires) {
			delete(m.flows, key)
		}
	}
	for key, s := range m.sessions {
		if !now.Before(s.expires) {
			s.cancel()
			delete(m.sessions, key)
		}
	}
}

func (m *Manager) cookie(w http.ResponseWriter, name, value string, age int, site http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: m.config.Webroot, HttpOnly: true, Secure: m.secure, SameSite: site, MaxAge: age})
}

func (m *Manager) sameOrigin(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return strings.EqualFold(u.Scheme, m.origin.Scheme) && originHost(u) == originHost(m.origin)
}

func originHost(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func (m *Manager) safeReturnTo(raw string) string {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") || !strings.HasPrefix(u.Path, m.config.Webroot) || strings.HasPrefix(u.Path, m.config.Webroot+"auth/") {
		return m.config.Webroot
	}
	return u.RequestURI()
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
