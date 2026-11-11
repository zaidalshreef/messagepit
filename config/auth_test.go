package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/auth"
	"github.com/tg123/go-htpasswd"
)

func resetAuthentication(t *testing.T) {
	t.Helper()

	uiFile, sendFile, smtpFile, pop3File := UIAuthFile, SendAPIAuthFile, SMTPAuthFile, POP3AuthFile
	uiAuth, sendAuth, smtpAuth, pop3Auth := auth.UICredentials, auth.SendAPICredentials, auth.SMTPCredentials, auth.POP3Credentials
	smtpInsecure, smtpStartTLS := SMTPAuthAllowInsecure, SMTPRequireSTARTTLS
	sendAny, smtpAny := SendAPIAuthAcceptAny, SMTPAuthAcceptAny
	webroot, tenantID, csp := Webroot, TenantID, ContentSecurityPolicy
	t.Cleanup(func() {
		UIAuthFile, SendAPIAuthFile, SMTPAuthFile, POP3AuthFile = uiFile, sendFile, smtpFile, pop3File
		auth.UICredentials, auth.SendAPICredentials, auth.SMTPCredentials, auth.POP3Credentials = uiAuth, sendAuth, smtpAuth, pop3Auth
		SMTPAuthAllowInsecure, SMTPRequireSTARTTLS = smtpInsecure, smtpStartTLS
		SendAPIAuthAcceptAny, SMTPAuthAcceptAny = sendAny, smtpAny
		Webroot, TenantID, ContentSecurityPolicy = webroot, tenantID, csp
	})

	UIAuthFile, SendAPIAuthFile, SMTPAuthFile, POP3AuthFile = "", "", "", ""
	auth.UICredentials, auth.SendAPICredentials, auth.SMTPCredentials, auth.POP3Credentials = nil, nil, nil, nil
	SMTPAuthAllowInsecure, SMTPRequireSTARTTLS = true, false
	SendAPIAuthAcceptAny, SMTPAuthAcceptAny = false, false
}

func TestAuthenticationPasswordFiles(t *testing.T) {
	services := []struct {
		name        string
		file        *string
		credentials **htpasswd.File
	}{
		{"ui", &UIAuthFile, &auth.UICredentials},
		{"send-api", &SendAPIAuthFile, &auth.SendAPICredentials},
		{"smtp", &SMTPAuthFile, &auth.SMTPCredentials},
		{"pop3", &POP3AuthFile, &auth.POP3Credentials},
	}

	for _, service := range services {
		t.Run(service.name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				contents string
				valid    bool
			}{
				{"empty", "", false},
				{"whitespace", " \t\n\r ", false},
				{"valid", "testuser:testpassword\n", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					resetAuthentication(t)
					path := filepath.Join(t.TempDir(), "htpasswd")
					if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
						t.Fatal(err)
					}
					*service.file = path

					err := VerifyConfig()
					if !tc.valid {
						if err == nil || !strings.Contains(err.Error(), "credentials") {
							t.Fatalf("expected an empty-credentials error; got %v", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("valid authentication file rejected: %v", err)
					}
					if *service.credentials == nil || !(*service.credentials).Match("testuser", "testpassword") {
						t.Fatal("credentials from the configured file do not authenticate")
					}
				})
			}
		})
	}
}

func TestAuthenticationRemainsOptionalWithoutCredentialFiles(t *testing.T) {
	resetAuthentication(t)
	if err := VerifyConfig(); err != nil {
		t.Fatalf("standalone configuration was rejected: %v", err)
	}
	if auth.UICredentials != nil || auth.SendAPICredentials != nil || auth.SMTPCredentials != nil || auth.POP3Credentials != nil {
		t.Fatal("authentication was enabled without credentials")
	}
}
