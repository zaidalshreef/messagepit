package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	jose "github.com/go-jose/go-jose/v4"
)

func TestServiceTokenIsRestrictedToEnvironmentAPI(t *testing.T) {
	m := &Manager{config: Config{Webroot: "/"}}
	m.SetTokenValidator(func(_ context.Context, token string) (ServicePrincipal, error) {
		if token != "valid" {
			return ServicePrincipal{}, errors.New("invalid")
		}
		return ServicePrincipal{EnvironmentID: "env-a", Role: access.Viewer}, nil
	})
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := CurrentServicePrincipal(r.Context())
		if !ok || principal.EnvironmentID != "env-a" || principal.Role != access.Viewer {
			t.Errorf("invalid service principal: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		path, token string
		want        int
	}{
		{"/api/v1/environments/env-a/messages", "valid", http.StatusNoContent},
		{"/api/v1/environments/env-a/messages", "invalid", http.StatusUnauthorized},
		{"/api/v1/messages", "valid", http.StatusUnauthorized},
		{"/api/v1/groups", "valid", http.StatusUnauthorized},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s returned %d, want %d", tc.path, w.Code, tc.want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/env-a/messages", nil)
	r.Header.Set("Authorization", "Bearer valid")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("insecure remote request with forged forwarded headers returned %d", w.Code)
	}
}

type fakeProvider struct {
	server                *httptest.Server
	key                   *rsa.PrivateKey
	signingKey            *rsa.PrivateKey
	nonce                 string
	challenge             string
	subject               string
	audience              any
	authorizedParty       string
	expires               time.Time
	insecureTokenEndpoint bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, subject: "user-1", audience: "test-client", expires: time.Now().Add(time.Hour)}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			tokenEndpoint := p.server.URL + "/token"
			if p.insecureTokenEndpoint {
				tokenEndpoint = "http://untrusted.example/token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize",
				"token_endpoint": tokenEndpoint, "jwks_uri": p.server.URL + "/keys",
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			_ = r.ParseForm()
			digest := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if r.FormValue("code") != "valid-code" || base64.RawURLEncoding.EncodeToString(digest[:]) != p.challenge {
				http.Error(w, "invalid code or verifier", http.StatusBadRequest)
				return
			}
			opts := (&jose.SignerOptions{}).WithHeader("kid", "test")
			signingKey := p.key
			if p.signingKey != nil {
				signingKey = p.signingKey
			}
			signer, signErr := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, opts)
			if signErr != nil {
				http.Error(w, "signer error", http.StatusInternalServerError)
				return
			}
			claims, _ := json.Marshal(map[string]any{
				"iss": p.server.URL, "sub": p.subject, "aud": p.audience, "nonce": p.nonce,
				"azp": p.authorizedParty, "iat": time.Now().Unix(), "exp": p.expires.Unix(),
			})
			object, signErr := signer.Sign(claims)
			if signErr != nil {
				http.Error(w, "signing error", http.StatusInternalServerError)
				return
			}
			raw, _ := object.CompactSerialize()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-token", "token_type": "Bearer", "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func testManager(t *testing.T, provider *fakeProvider) *Manager {
	t.Helper()
	m, err := New(context.Background(), Config{
		Issuer: provider.server.URL, ClientID: "test-client", PublicURL: "http://127.0.0.1:8025",
		AllowedSubjects: "user-1", AdminSubjects: "user-1", Webroot: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func beginSignIn(t *testing.T, handler http.Handler, provider *fakeProvider, returnTo string) (string, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025/auth/login?return_to="+url.QueryEscape(returnTo), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("login returned %d: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "test-client" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request is incomplete: %s", u.RawQuery)
	}
	provider.nonce, provider.challenge = q.Get("nonce"), q.Get("code_challenge")
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == stateCookie {
			return q.Get("state"), cookie
		}
	}
	t.Fatal("state cookie missing")
	return "", nil
}

func completeSignIn(handler http.Handler, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025/auth/callback?code=valid-code&state="+url.QueryEscape(state), nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestSignInProtectsRequestsAndLogoutRevokesSession(t *testing.T) {
	p := newFakeProvider(t)
	m := testManager(t, p)
	var auditEvents []string
	m.SetAuditHook(func(principal Principal, action string) {
		if principal.Subject != "user-1" {
			t.Errorf("wrong session audit principal: %+v", principal)
		}
		auditEvents = append(auditEvents, action)
	})
	var lifetime context.Context
	var principal Principal
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lifetime = Lifetime(r.Context())
		principal, _ = CurrentPrincipal(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025/api/v1/messages", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous API request returned %d", w.Code)
	}

	state, stateCookieValue := beginSignIn(t, handler, p, "/sms?query=test")
	if bad := completeSignIn(handler, state, nil); bad.Code != http.StatusUnauthorized {
		t.Fatalf("callback without state cookie returned %d", bad.Code)
	}
	w = completeSignIn(handler, state, stateCookieValue)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/sms?query=test" {
		t.Fatalf("valid sign-in returned %d, location %q, body %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	var browserSession *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			browserSession = cookie
		}
	}
	if browserSession == nil || !browserSession.HttpOnly || browserSession.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie is missing required protections: %+v", browserSession)
	}
	if browserSession.MaxAge > 3600 || browserSession.MaxAge <= 0 {
		t.Fatalf("session outlives the identity token: %d", browserSession.MaxAge)
	}
	if replay := completeSignIn(handler, state, stateCookieValue); replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed callback returned %d", replay.Code)
	}

	request := func(method, target, origin string) int {
		r := httptest.NewRequest(method, "http://127.0.0.1:8025"+target, nil)
		r.AddCookie(browserSession)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(http.MethodGet, "/api/v1/messages", ""); got != http.StatusNoContent || lifetime == nil {
		t.Fatalf("authorized request returned %d", got)
	}
	if principal.Issuer != p.server.URL || principal.Subject != "user-1" || !principal.Admin {
		t.Fatalf("incorrect authenticated principal: %+v", principal)
	}
	if got := request(http.MethodDelete, "/api/v1/messages", "http://attacker.example"); got != http.StatusForbidden {
		t.Fatalf("cross-origin modification returned %d", got)
	}
	ws := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025/api/events", nil)
	ws.AddCookie(browserSession)
	ws.Header.Set("Origin", "http://attacker.example")
	ws.Header.Set("Upgrade", "websocket")
	wsResult := httptest.NewRecorder()
	handler.ServeHTTP(wsResult, ws)
	if wsResult.Code != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket returned %d", wsResult.Code)
	}
	if got := request(http.MethodDelete, "/api/v1/messages", "http://127.0.0.1:8025"); got != http.StatusNoContent {
		t.Fatalf("same-origin modification returned %d", got)
	}
	if got := request(http.MethodPost, "/auth/logout", "http://127.0.0.1:8025"); got != http.StatusNoContent {
		t.Fatalf("logout returned %d", got)
	}
	select {
	case <-lifetime.Done():
	case <-time.After(time.Second):
		t.Fatal("logout did not revoke the session lifetime")
	}
	if got := request(http.MethodGet, "/api/v1/messages", ""); got != http.StatusUnauthorized {
		t.Fatalf("revoked session returned %d", got)
	}
	if len(auditEvents) != 2 || auditEvents[0] != "session.login" || auditEvents[1] != "session.logout" {
		t.Fatalf("session audit events: %v", auditEvents)
	}
}

func TestSignInRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *fakeProvider)
		want      int
	}{
		{"wrong nonce", func(_ *testing.T, p *fakeProvider) { p.nonce = "wrong" }, http.StatusUnauthorized},
		{"wrong audience", func(_ *testing.T, p *fakeProvider) { p.audience = "other-client" }, http.StatusUnauthorized},
		{"missing authorized party", func(_ *testing.T, p *fakeProvider) { p.audience = []string{"test-client", "another"} }, http.StatusUnauthorized},
		{"wrong authorized party", func(_ *testing.T, p *fakeProvider) { p.authorizedParty = "other-client" }, http.StatusUnauthorized},
		{"expired token", func(_ *testing.T, p *fakeProvider) { p.expires = time.Now().Add(-time.Hour) }, http.StatusUnauthorized},
		{"invalid signature", func(t *testing.T, p *fakeProvider) {
			var err error
			p.signingKey, err = rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
		}, http.StatusUnauthorized},
		{"unlisted subject", func(_ *testing.T, p *fakeProvider) { p.subject = "other-user" }, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakeProvider(t)
			m := testManager(t, p)
			handler := m.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			state, cookie := beginSignIn(t, handler, p, "//attacker.example/")
			tt.configure(t, p)
			w := completeSignIn(handler, state, cookie)
			if w.Code != tt.want {
				t.Fatalf("invalid identity returned %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestNonAdministratorCannotAccessLegacyMailbox(t *testing.T) {
	p := newFakeProvider(t)
	m, err := New(context.Background(), Config{
		Issuer: p.server.URL, ClientID: "test-client", PublicURL: "http://127.0.0.1:8025",
		AllowedSubjects: "user-1", Webroot: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	state, stateCookieValue := beginSignIn(t, handler, p, "/")
	signedIn := completeSignIn(handler, state, stateCookieValue)
	var browserSession *http.Cookie
	for _, cookie := range signedIn.Result().Cookies() {
		if cookie.Name == sessionCookie {
			browserSession = cookie
		}
	}
	if browserSession == nil {
		t.Fatalf("sign-in failed: %d %s", signedIn.Code, signedIn.Body.String())
	}
	for _, path := range []string{"/api/v1/messages", "/api/v1/sms/messages", "/api/v1/webhooks", "/api/events", "/proxy"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025"+path, nil)
		r.AddCookie(browserSession)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("non-administrator accessed %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/groups", "/api/v1/me", "/api/v1/groups", "/api/v1/projects/p1", "/dist/app.js"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025"+path, nil)
		r.AddCookie(browserSession)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("non-administrator could not reach %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8025/", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(browserSession)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/groups" {
		t.Fatalf("non-administrator landing page: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestConfigAndRedirectValidation(t *testing.T) {
	cfg := Config{Issuer: "https://identity.example", ClientID: "client", PublicURL: "https://app.example", AllowedSubjects: "user-1", Webroot: "/team/"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Issuer = "http://identity.example" },
		func(c *Config) { c.PublicURL = "http://app.example" },
		func(c *Config) { c.AllowedSubjects = " , " },
		func(c *Config) { c.ClientID = "" },
		func(c *Config) { c.AdminSubjects = "other-user" },
	} {
		invalid := cfg
		change(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("unsafe configuration accepted: %+v", invalid)
		}
	}
	m := &Manager{config: cfg}
	for _, target := range []string{"//attacker.example", "https://attacker.example", "/other", "/team/\\evil", "/team/auth/logout"} {
		if got := m.safeReturnTo(target); got != "/team/" {
			t.Errorf("unsafe return target %q became %q", target, got)
		}
	}
	if got := m.safeReturnTo("/team/sms?q=test"); !strings.HasPrefix(got, "/team/sms?") {
		t.Fatalf("valid return target became %q", got)
	}
}

func TestProviderRejectsInsecureEndpoints(t *testing.T) {
	p := newFakeProvider(t)
	p.insecureTokenEndpoint = true
	_, err := New(context.Background(), Config{
		Issuer: p.server.URL, ClientID: "test-client", PublicURL: "http://127.0.0.1:8025",
		AllowedSubjects: "user-1", Webroot: "/",
	})
	if err == nil {
		t.Fatal("insecure provider token endpoint was accepted")
	}
}
