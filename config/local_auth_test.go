package config

import (
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/ai"
	"github.com/coreydaley/messagepit/internal/auth"
	"github.com/coreydaley/messagepit/internal/oidcauth"
)

func TestLocalAuthenticationConfiguration(t *testing.T) {
	resetAuthentication(t)
	oldLocal, oldURL, oldPassword, oldOIDC, oldDB, oldAI := LocalAuth, LocalPublicURL, LocalAdminPassword, OIDC, Database, AI
	t.Cleanup(func() {
		LocalAuth, LocalPublicURL, LocalAdminPassword, OIDC, Database, AI = oldLocal, oldURL, oldPassword, oldOIDC, oldDB, oldAI
	})
	LocalAuth, LocalPublicURL, LocalAdminPassword, OIDC, Database, AI = true, "http://127.0.0.1:8025", "", oidcauth.Config{}, "", ai.Config{}
	if err := VerifyConfig(); err != nil || !SharedAuthEnabled() {
		t.Fatalf("local authentication configuration rejected: %v", err)
	}
	LocalPublicURL = "http://remote.example"
	if err := VerifyConfig(); err == nil {
		t.Fatal("insecure remote local-login origin was accepted")
	}
	LocalPublicURL = "https://sandbox.example"
	OIDC = oidcauth.Config{Issuer: "https://identity.example", ClientID: "client", PublicURL: "https://sandbox.example", AllowedSubjects: "alice"}
	if err := VerifyConfig(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatal("local and OIDC authentication were enabled together")
	}
	OIDC = oidcauth.Config{}
	if err := auth.SetUIAuth("user:password"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyConfig(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatal("local authentication accepted conflicting UI credentials")
	}
	auth.UICredentials = nil
	Database = "https://rqlite.example:4001"
	if err := VerifySharedDatabase(); err == nil {
		t.Fatal("local project access accepted nontransactional storage")
	}
	Database = ""
	AI = ai.Config{BaseURL: "http://127.0.0.1:1234/v1", Model: "test"}
	if err := VerifyConfig(); err != nil {
		t.Fatalf("local shared access rejected valid optional AI configuration: %v", err)
	}
	LocalAuth, LocalAdminPassword, AI = false, "unexpected-secret", ai.Config{}
	if err := VerifyConfig(); err == nil {
		t.Fatal("bootstrap password was silently ignored while local authentication was disabled")
	}
}
