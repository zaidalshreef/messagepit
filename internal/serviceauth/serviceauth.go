// Package serviceauth authenticates short-lived credentials used by ingest adapters.
package serviceauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/coreydaley/messagepit/internal/transport"
)

var ErrUnauthorized = errors.New("invalid service credential")

// Authenticate checks a credential and the write permission needed for capture.
func Authenticate(ctx context.Context, secret string) (storage.ServiceToken, error) {
	token, err := storage.AuthenticateServiceToken(ctx, secret)
	if err != nil || !token.Role.Allows(access.WriteMessages) {
		return storage.ServiceToken{}, ErrUnauthorized
	}
	return token, nil
}

// Bearer authenticates an ingest request without trusting forwarded headers.
func Bearer(r *http.Request) (storage.ServiceToken, error) {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !transport.SecureOrLoopback(r) {
		return storage.ServiceToken{}, ErrUnauthorized
	}
	return Authenticate(r.Context(), value)
}

// Basic authenticates a Twilio-compatible request using its account and token.
func Basic(r *http.Request, account string) (storage.ServiceToken, error) {
	username, password, ok := r.BasicAuth()
	if !ok || username != account || !transport.SecureOrLoopback(r) {
		return storage.ServiceToken{}, ErrUnauthorized
	}
	return Authenticate(r.Context(), password)
}
