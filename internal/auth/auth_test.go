package auth

import (
	"strings"
	"testing"

	"github.com/tg123/go-htpasswd"
)

func TestCredentialSettersRejectEmptyInput(t *testing.T) {
	tests := []struct {
		name        string
		set         func(string) error
		credentials **htpasswd.File
	}{
		{"ui", SetUIAuth, &UICredentials},
		{"send API", SetSendAPIAuth, &SendAPICredentials},
		{"SMTP", SetSMTPAuth, &SMTPCredentials},
		{"POP3", SetPOP3Auth, &POP3Credentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := *tt.credentials
			t.Cleanup(func() { *tt.credentials = original })

			if err := tt.set("testuser:testpassword"); err != nil {
				t.Fatalf("set valid credentials: %v", err)
			}
			valid := *tt.credentials
			if valid == nil || !valid.Match("testuser", "testpassword") {
				t.Fatal("valid credentials cannot authenticate")
			}

			for _, input := range []struct{ name, value string }{
				{"empty", ""},
				{"whitespace", " \t\n\r "},
				{"oversized", strings.Repeat("x", 70*1024)},
			} {
				t.Run(input.name, func(t *testing.T) {
					if err := tt.set(input.value); err == nil {
						t.Error("expected invalid credentials to be rejected")
					}
					if *tt.credentials != valid || !(*tt.credentials).Match("testuser", "testpassword") {
						t.Fatal("invalid input replaced the previously valid credentials")
					}
				})
			}
		})
	}
}
