package apiv1

import (
	"context"
	"net/http"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/scopedevents"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/websocket"
)

var environmentUpgrader = websocket.Upgrader{
	// Browser sessions are subject to the OIDC middleware's public-origin check.
	// Bearer credentials are explicit and do not rely on ambient browser cookies.
	CheckOrigin: func(*http.Request) bool { return true },
}

// EnvironmentEvents delivers content-free change signals for one authorized
// environment. The subscriber must fetch records using the scoped API.
func EnvironmentEvents(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	principal, hasPrincipal := oidcauth.CurrentPrincipal(r.Context())
	service, hasService := oidcauth.CurrentServicePrincipal(r.Context())
	lifetime := oidcauth.Lifetime(r.Context())
	authorized := func() bool {
		if lifetime != nil && lifetime.Err() != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if hasService {
			token, err := storage.ActiveServiceToken(ctx, service.ID)
			return err == nil && token.EnvironmentID == environment.ID && token.Role.Allows(access.ReadMessages)
		}
		if !hasPrincipal {
			return false
		}
		if principal.Issuer == oidcauth.LocalIssuer && !storage.LocalUserActive(ctx, principal) {
			return false
		}
		if principal.Admin {
			return true
		}
		role, err := storage.ProjectRole(ctx, environment.ProjectID, principal.Issuer, principal.Subject)
		return err == nil && role.Allows(access.ReadMessages)
	}

	updates, unsubscribe, err := scopedevents.Subscribe(environment.ID)
	if err != nil {
		http.Error(w, "Live updates are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	if !authorized() {
		http.NotFound(w, r)
		return
	}
	conn, err := environmentUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// A reader is required to handle WebSocket pong and close frames. There is
	// only one writer, in the loop below.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		conn.SetReadLimit(1024)
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	var sessionDone <-chan struct{}
	if lifetime != nil {
		sessionDone = lifetime.Done()
		stop := context.AfterFunc(lifetime, func() { _ = conn.Close() })
		defer stop()
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		var messageType int
		var payload []byte
		select {
		case <-updates:
			messageType, payload = websocket.TextMessage, []byte("changed")
		case <-ticker.C:
			messageType = websocket.PingMessage
		case <-sessionDone:
			return
		case <-r.Context().Done():
			return
		case <-readDone:
			return
		}
		// Permissions and tokens can be revoked while a WebSocket is open.
		if !authorized() {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(messageType, payload); err != nil {
			return
		}
	}
}
