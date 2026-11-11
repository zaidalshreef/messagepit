package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
)

func TestServiceTokensAreScopedShortLivedAndRevocable(t *testing.T) {
	setup("tokens_test")
	defer Close()
	ctx := context.Background()
	issued, value, err := IssueServiceToken(ctx, LegacyEnvironmentID, "CI", access.Developer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if value == "" || issued.EnvironmentID != LegacyEnvironmentID || issued.Role != access.Developer {
		t.Fatalf("unexpected token: %+v", issued)
	}
	authenticated, err := AuthenticateServiceToken(ctx, value)
	if err != nil || authenticated.ID != issued.ID || authenticated.EnvironmentID != LegacyEnvironmentID {
		t.Fatalf("valid token rejected: %+v, %v", authenticated, err)
	}
	mutated := value[:len(value)-1] + "A"
	if mutated == value {
		mutated = value[:len(value)-1] + "B"
	}
	for _, invalid := range []string{"", value + ".extra", mutated, "mpt_unknown." + strings.Repeat("A", 43)} {
		if _, err := AuthenticateServiceToken(ctx, invalid); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("invalid token was accepted: %v", err)
		}
	}
	tokens, err := ListServiceTokens(ctx, LegacyEnvironmentID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list tokens: %+v, %v", tokens, err)
	}
	encoded, err := json.Marshal(tokens)
	if err != nil || strings.Contains(string(encoded), value) {
		t.Fatalf("token listing exposed a secret: %s, %v", encoded, err)
	}
	if err := RevokeServiceToken(ctx, "another-environment", issued.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("cross-environment revocation: %v", err)
	}
	if err := RevokeServiceToken(ctx, LegacyEnvironmentID, issued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateServiceToken(ctx, value); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token was accepted: %v", err)
	}
	expired, expiredValue, err := IssueServiceToken(ctx, LegacyEnvironmentID, "Expired", access.Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	query := `UPDATE ` + tenant("service_tokens") + ` SET Expires = ? WHERE ID = ?`
	if _, err := db.Exec(query, time.Now().Add(-time.Minute).UnixMilli(), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateServiceToken(ctx, expiredValue); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token was accepted: %v", err)
	}

	for _, request := range []struct {
		role access.Role
		ttl  time.Duration
	}{
		{access.Admin, time.Hour},
		{access.Viewer, 0},
		{access.Developer, 25 * time.Hour},
	} {
		if _, _, err := IssueServiceToken(ctx, LegacyEnvironmentID, "CI", request.role, request.ttl); !errors.Is(err, ErrInvalidTokenRequest) {
			t.Errorf("invalid token request accepted: %v", err)
		}
	}
	if _, _, err := IssueServiceToken(ctx, "missing", "CI", access.Viewer, time.Hour); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("token issued for missing environment: %v", err)
	}
}
