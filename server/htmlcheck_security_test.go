package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestScopedHTMLCheckRespectsNetworkPolicy(t *testing.T) {
	setup()
	defer storage.Close()
	previousAllow, previousBlock := config.AllowInternalHTTPRequests, config.BlockRemoteCSSAndFonts
	config.AllowInternalHTTPRequests, config.BlockRemoteCSSAndFonts = false, false
	defer func() { config.AllowInternalHTTPRequests, config.BlockRemoteCSSAndFonts = previousAllow, previousBlock }()

	var requests atomic.Int32
	stylesheet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte("p { color: red; }"))
	}))
	defer stylesheet.Close()

	ctx := context.Background()
	issuer := "https://id.example"
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, storage.LegacyGroupID, issuer, subject); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.GrantProjectRole(ctx, storage.LegacyProjectID, issuer, "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	email := []byte(fmt.Sprintf("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: stylesheet test\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><link rel=\"stylesheet\" href=\"%s/private?token=private-token\"></head><body><p>test</p></body></html>", stylesheet.URL))
	id, err := storage.Store(&email, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := apiRoutes()
	request := func(subject string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+storage.LegacyEnvironmentID+"/messages/"+id+"/html-check", nil)
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), oidcauth.Principal{Issuer: issuer, Subject: subject}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	if request("bob") != http.StatusNotFound || requests.Load() != 0 {
		t.Fatal("group membership alone allowed HTML inspection")
	}
	if request("alice") != http.StatusOK || requests.Load() != 0 {
		t.Fatal("authorized HTML inspection reached an internal stylesheet without opt-in")
	}
	config.AllowInternalHTTPRequests = true
	if request("alice") != http.StatusOK || requests.Load() != 1 {
		t.Fatal("authorized HTML inspection could not use an explicitly allowed internal stylesheet")
	}
}
