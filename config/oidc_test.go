package config

import (
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/auth"
	"github.com/coreydaley/messagepit/internal/oidcauth"
)

func TestOIDCRejectsConflictingAuthentication(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T)
	}{
		{"UI credentials", func(t *testing.T) {
			if err := auth.SetUIAuth("user:password"); err != nil {
				t.Fatal(err)
			}
		}},
		{"send API credentials", func(t *testing.T) {
			if err := auth.SetSendAPIAuth("user:password"); err != nil {
				t.Fatal(err)
			}
		}},
		{"send API accepts any credentials", func(*testing.T) { SendAPIAuthAcceptAny = true }},
		{"SMTP credentials", func(t *testing.T) {
			SMTPAuthAllowInsecure = true
			if err := auth.SetSMTPAuth("user:password"); err != nil {
				t.Fatal(err)
			}
		}},
		{"SMTP accepts any credentials", func(*testing.T) { SMTPAuthAllowInsecure = true; SMTPAuthAcceptAny = true }},
		{"POP3 credentials", func(t *testing.T) {
			if err := auth.SetPOP3Auth("user:password"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetAuthentication(t)
			previous := OIDC
			t.Cleanup(func() { OIDC = previous })
			OIDC = oidcauth.Config{Issuer: "https://identity.example", ClientID: "client", PublicURL: "https://app.example", AllowedSubjects: "user-1"}
			tt.setup(t)
			if err := VerifyConfig(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("conflicting authentication was accepted: %v", err)
			}
		})
	}
}

func TestOIDCRejectsNonTransactionalRqlite(t *testing.T) {
	resetAuthentication(t)
	previousOIDC, previousDatabase := OIDC, Database
	t.Cleanup(func() { OIDC, Database = previousOIDC, previousDatabase })
	OIDC = oidcauth.Config{Issuer: "https://identity.example", ClientID: "client", PublicURL: "https://app.example", AllowedSubjects: "user-1"}
	Database = "https://rqlite.example:4001"
	if err := VerifyConfig(); err == nil || !strings.Contains(err.Error(), "requires SQLite") {
		t.Fatalf("shared mode accepted non-transactional rqlite: %v", err)
	}
	OIDC = oidcauth.Config{}
	if err := VerifySharedDatabase(); err != nil {
		t.Fatalf("standalone rqlite was rejected: %v", err)
	}
}
