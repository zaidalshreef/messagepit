package oidcauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalSignInUsesProtectedSessions(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	manager, err := NewLocal("http://127.0.0.1:8025", "/", func(_ context.Context, user, password string) (Principal, error) {
		if user != "alice" || password != "valid-password" {
			return Principal{}, errors.New("invalid credentials")
		}
		return Principal{Issuer: LocalIssuer, Subject: user, LocalVersion: 1}, nil
	}, func(context.Context, Principal) bool { return active.Load() })
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentPrincipal(r.Context()); !ok {
			t.Error("authenticated request has no principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(method, path string, form url.Values, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8025"+path, strings.NewReader(form.Encode()))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	page := request(http.MethodGet, "/auth/login", nil, "", nil)
	if page.Code != http.StatusOK || page.Header().Get("Referrer-Policy") != "same-origin" || !strings.Contains(page.Body.String(), `name="username"`) || strings.Contains(page.Body.String(), "SSO") {
		t.Fatal("local sign-in page was not displayed")
	}
	form := url.Values{"username": {"alice"}, "password": {"valid-password"}, "return_to": {"/projects"}}
	if w := request(http.MethodPost, "/auth/login", form, "https://attacker.example", nil); w.Code != http.StatusForbidden {
		t.Fatal("cross-origin sign-in was accepted")
	}
	bad := url.Values{"username": {"alice"}, "password": {"private-wrong-password"}}
	if w := request(http.MethodPost, "/auth/login", bad, "http://127.0.0.1:8025", nil); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "private-wrong-password") {
		t.Fatal("invalid local credentials were not safely rejected")
	}
	login := request(http.MethodPost, "/auth/login", form, "http://127.0.0.1:8025", nil)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/projects" {
		t.Fatalf("local sign-in returned %d", login.Code)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("local sign-in did not issue a protected session cookie")
	}
	if w := request(http.MethodGet, "/api/v1/me", nil, "", cookies[0]); w.Code != http.StatusNoContent {
		t.Fatal("local session was not accepted")
	}
	active.Store(false)
	if w := request(http.MethodGet, "/api/v1/me", nil, "", cookies[0]); w.Code != http.StatusUnauthorized {
		t.Fatal("disabled account retained a session")
	}
}

func TestLocalSignInRateLimitAndSecureOrigin(t *testing.T) {
	manager, err := NewLocal("https://sandbox.example", "/", func(context.Context, string, string) (Principal, error) {
		return Principal{}, errors.New("invalid")
	}, func(context.Context, Principal) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	handler := manager.Wrap(http.NotFoundHandler())
	remote := httptest.NewRequest(http.MethodGet, "http://sandbox.example/auth/login", nil)
	remote.RemoteAddr = "203.0.113.5:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, remote)
	if w.Code != http.StatusForbidden {
		t.Fatal("remote plaintext sign-in was allowed")
	}
	for i := 0; i < 11; i++ {
		form := url.Values{"username": {"alice"}, "password": {"invalid-password"}}
		r := httptest.NewRequest(http.MethodPost, "https://sandbox.example/auth/login", strings.NewReader(form.Encode()))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://sandbox.example")
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if i < 10 && w.Code != http.StatusUnauthorized || i == 10 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("sign-in attempt %d returned %d", i, w.Code)
		}
	}
	if _, err := NewLocal("http://sandbox.example", "/", manager.localLogin, manager.localValidate); err == nil {
		t.Fatal("insecure remote public URL was accepted")
	}
}
