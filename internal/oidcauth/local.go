package oidcauth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coreydaley/messagepit/internal/transport"
	"golang.org/x/time/rate"
)

// LocalIssuer keeps local user grants separate from external identity providers.
const LocalIssuer = "messagepit:local"

type LocalLogin func(context.Context, string, string) (Principal, error)
type LocalValidate func(context.Context, Principal) bool

type localAttempt struct {
	limiter *rate.Limiter
	last    time.Time
}

// ValidateLocalConfig applies the same secure-origin policy as OIDC sessions.
func ValidateLocalConfig(publicURL, webroot string) error {
	if _, err := secureURL(publicURL, false); err != nil {
		return fmt.Errorf("local authentication public URL: %w", err)
	}
	if !strings.HasPrefix(webroot, "/") || !strings.HasSuffix(webroot, "/") || strings.HasPrefix(webroot, "//") {
		return errors.New("local authentication requires a normalized webroot")
	}
	return nil
}

// NewLocal creates the existing session manager with a local password boundary.
func NewLocal(publicURL, webroot string, login LocalLogin, validate LocalValidate) (*Manager, error) {
	if err := ValidateLocalConfig(publicURL, webroot); err != nil {
		return nil, err
	}
	if login == nil || validate == nil {
		return nil, errors.New("local authentication requires account checks")
	}
	origin, _ := secureURL(publicURL, false)
	return &Manager{
		config: Config{PublicURL: publicURL, Webroot: webroot}, origin: origin, secure: origin.Scheme == "https",
		localLogin: login, localValidate: validate, localAttempts: make(map[string]*localAttempt), loginSlots: make(chan struct{}, 4),
		flows: make(map[string]flow), sessions: make(map[[32]byte]*session),
	}, nil
}

// allowLocalAttempt bounds password verification per direct client and in total.
// Forwarded headers are deliberately not trusted.
func (m *Manager) allowLocalAttempt(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = "unknown"
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	attempt := m.localAttempts[host]
	if attempt == nil {
		if len(m.localAttempts) >= maxEntries {
			for key, entry := range m.localAttempts {
				if now.Sub(entry.last) > 10*time.Minute {
					delete(m.localAttempts, key)
				}
			}
			if len(m.localAttempts) >= maxEntries {
				return false
			}
		}
		attempt = &localAttempt{limiter: rate.NewLimiter(rate.Every(3*time.Second), 10)}
		m.localAttempts[host] = attempt
	}
	attempt.last = now
	return attempt.limiter.Allow()
}

func (m *Manager) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if !transport.SecureOrLoopback(r) {
		http.Error(w, "Secure connection required", http.StatusForbidden)
		return
	}
	returnTo := m.safeReturnTo(r.URL.Query().Get("return_to"))
	switch r.Method {
	case http.MethodGet:
		m.localLoginPage(w, http.StatusOK, returnTo, false)
		return
	case http.MethodPost:
		if !m.sameOrigin(r) {
			http.Error(w, "Invalid origin", http.StatusForbidden)
			return
		}
		if !m.allowLocalAttempt(r) {
			http.Error(w, "Too many sign-in attempts; try again later", http.StatusTooManyRequests)
			return
		}
		select {
		case m.loginSlots <- struct{}{}:
			defer func() { <-m.loginSlots }()
		default:
			http.Error(w, "Too many sign-in attempts; try again later", http.StatusTooManyRequests)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil || len(r.PostForm["username"]) != 1 || len(r.PostForm["password"]) != 1 || len(r.PostForm["return_to"]) > 1 {
			http.Error(w, "Invalid sign-in request", http.StatusBadRequest)
			return
		}
		returnTo = m.safeReturnTo(r.PostForm.Get("return_to"))
		principal, err := m.localLogin(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"))
		if err != nil {
			m.localLoginPage(w, http.StatusUnauthorized, returnTo, true)
			return
		}
		m.startSession(w, r, principal, time.Now().Add(sessionLife), returnTo)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) localLoginPage(w http.ResponseWriter, status int, returnTo string, failed bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A no-referrer policy makes browsers send Origin: null on form POSTs,
	// preventing the same-origin CSRF check from accepting the login form.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	message := ""
	if failed {
		message = `<p class="error" role="alert">Invalid username or password.</p>`
	}
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · MessagePit</title><style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f5f3f7;color:#241c33;font:16px system-ui,sans-serif}main{box-sizing:border-box;width:min(92%%,420px);background:white;border-top:5px solid #25935f;border-radius:8px;padding:2rem;box-shadow:0 8px 28px #241c3320}h1{margin:0 0 .4rem;font-size:1.75rem}p{line-height:1.5}label{display:block;font-weight:600;margin:1rem 0 .4rem}input,button{box-sizing:border-box;width:100%%;font:inherit;padding:.7rem;border-radius:5px}input{border:1px solid #8b8294}button{margin-top:1.4rem;border:0;background:#413258;color:white;font-weight:700;cursor:pointer}button:focus-visible,input:focus-visible{outline:3px solid #25935f;outline-offset:2px}.error{color:#a21c24}
</style></head><body><main><h1>MessagePit</h1><p>Sign in to your developer sandbox.</p>%s<form method="post" action="%s"><input type="hidden" name="return_to" value="%s"><label for="username">Username</label><input id="username" name="username" autocomplete="username" required maxlength="64"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required maxlength="72"><button type="submit">Sign in</button></form></main></body></html>`, message, html.EscapeString(m.config.Webroot+"auth/login"), html.EscapeString(returnTo))
}
