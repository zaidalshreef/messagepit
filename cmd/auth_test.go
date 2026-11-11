package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/auth"
	"github.com/tg123/go-htpasswd"
)

var authEnvironmentVariables = []string{"MP_UI_AUTH", "MP_SEND_API_AUTH", "MP_SMTP_AUTH", "MP_POP3_AUTH"}

func environmentWithoutAuthentication() []string {
	var result []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		isAuthVariable := name == "MESSAGEPIT_TEST_AUTH_ENV"
		for _, authVariable := range authEnvironmentVariables {
			if name == authVariable || name == authVariable+"_FILE" {
				isAuthVariable = true
				break
			}
		}
		if !isAuthVariable {
			result = append(result, value)
		}
	}
	return result
}

func TestBlankAuthenticationEnvironmentIsRejected(t *testing.T) {
	for _, variable := range authEnvironmentVariables {
		t.Run(variable, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^$")
			command.Env = append(environmentWithoutAuthentication(), variable+"= \t ")

			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("expected the process to reject a blank authentication value")
			}
			if !strings.Contains(string(output), variable) || !strings.Contains(string(output), "credentials") {
				t.Fatalf("expected an error naming the authentication variable; got %s", output)
			}
		})
	}
}

func TestAuthenticationEnvironmentLoadsCredentials(t *testing.T) {
	if variable := os.Getenv("MESSAGEPIT_TEST_AUTH_ENV"); variable != "" {
		credentials := map[string]*htpasswd.File{
			"MP_UI_AUTH":       auth.UICredentials,
			"MP_SEND_API_AUTH": auth.SendAPICredentials,
			"MP_SMTP_AUTH":     auth.SMTPCredentials,
			"MP_POP3_AUTH":     auth.POP3Credentials,
		}[variable]
		if credentials == nil || !credentials.Match("testuser", "testpassword") {
			t.Fatal("configured credentials did not authenticate")
		}
		return
	}

	for _, variable := range authEnvironmentVariables {
		t.Run(variable, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestAuthenticationEnvironmentLoadsCredentials$")
			command.Env = append(environmentWithoutAuthentication(),
				variable+"=testuser:testpassword", "MESSAGEPIT_TEST_AUTH_ENV="+variable)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("valid authentication environment failed: %v\n%s", err, output)
			}
		})
	}
}
