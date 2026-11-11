package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/websocket"
)

func TestEnvironmentEventsAreScopedAndRecheckAccess(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	const issuer = "https://id.example"
	group, err := storage.CreateGroup(ctx, "Live test")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, group.ID, issuer, subject); err != nil {
			t.Fatal(err)
		}
	}
	makeProject := func(name, subject string) (storage.Project, string) {
		t.Helper()
		p, err := storage.CreateProject(ctx, group.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.GrantProjectRole(ctx, p.ID, issuer, subject, access.Viewer); err != nil {
			t.Fatal(err)
		}
		envs, err := storage.ListProjectEnvironments(ctx, p.ID)
		if err != nil || len(envs) != 1 {
			t.Fatalf("project environments: %+v %v", envs, err)
		}
		return p, envs[0].ID
	}
	projectA, envA := makeProject("A", "alice")
	_, envB := makeProject("B", "bob")
	token, _, err := storage.IssueServiceToken(ctx, envA, "test", access.Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	router := apiRoutes()
	// Initialize server middleware before concurrent WebSocket requests.
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	var handlers sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		switch r.Header.Get("X-Test-Actor") {
		case "alice", "bob":
			r = r.WithContext(oidcauth.WithPrincipal(r.Context(), oidcauth.Principal{Issuer: issuer, Subject: r.Header.Get("X-Test-Actor")}))
		case "token":
			r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), oidcauth.ServicePrincipal{ID: token.ID, EnvironmentID: envA, Role: access.Viewer}))
		}
		router.ServeHTTP(w, r)
	}))
	defer func() { server.Close(); handlers.Wait() }()
	dial := func(env, actor string) (*websocket.Conn, *http.Response, error) {
		t.Helper()
		headers := http.Header{"X-Test-Actor": []string{actor}}
		return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/environments/"+env+"/events", headers)
	}
	if conn, response, err := dial(envB, "alice"); err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("group membership alone allowed subscription: %v %v", response, err)
	}
	alice, _, err := dial(envA, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	bob, _, err := dial(envB, "bob")
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	service, _, err := dial(envA, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	read := func(conn *websocket.Conn) (string, error) {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, payload, err := conn.ReadMessage()
		return string(payload), err
	}
	bobResult := make(chan string, 1)
	go func() {
		msg, err := read(bob)
		if err != nil {
			bobResult <- "error: " + err.Error()
			return
		}
		bobResult <- msg
	}()
	checkA := func() {
		t.Helper()
		for _, conn := range []*websocket.Conn{alice, service} {
			if msg, err := read(conn); err != nil || msg != "changed" {
				t.Fatalf("change signal: %q %v", msg, err)
			}
		}
	}
	email := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: test\r\n\r\nMessage")
	emailID, err := storage.StoreInEnvironment(ctx, envA, &email, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkA()
	if _, err := storage.StoreSMSInEnvironment(ctx, envA, "a", "b", "message", "", ""); err != nil {
		t.Fatal(err)
	}
	checkA()
	if _, err := storage.StoreWebhookInEnvironment(ctx, envA, "POST", "/", "", nil, []byte("message"), "text/plain", ""); err != nil {
		t.Fatal(err)
	}
	checkA()
	if err := storage.DeleteEnvironmentRecord(ctx, "email", emailID, envA); err != nil {
		t.Fatal(err)
	}
	checkA()
	select {
	case msg := <-bobResult:
		t.Fatalf("another environment received a signal: %s", msg)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := storage.StoreSMSInEnvironment(ctx, envB, "a", "b", "other", "", ""); err != nil {
		t.Fatal(err)
	}
	if msg := <-bobResult; msg != "changed" {
		t.Fatalf("other environment signal: %q", msg)
	}

	if err := storage.RevokeProjectRole(ctx, projectA.ID, issuer, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := storage.RevokeServiceToken(ctx, envA, token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.StoreSMSInEnvironment(ctx, envA, "a", "b", "revoked", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{alice, service} {
		if msg, err := read(conn); err == nil {
			t.Fatalf("revoked subscriber received %q", msg)
		}
	}
}
