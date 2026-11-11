package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/config"
)

func TestLocalAuthEnvironmentIsFailClosed(t *testing.T) {
	if os.Getenv("MESSAGEPIT_TEST_LOCAL_ENV") == "1" {
		if !config.LocalAuth || config.LocalAdminPassword != "test-bootstrap-password" {
			t.Fatal("local authentication environment was not loaded")
		}
		if _, exists := os.LookupEnv("MP_LOCAL_ADMIN_PASSWORD"); exists {
			t.Fatal("bootstrap password remained in the process environment")
		}
		return
	}
	base := []string{}
	for _, value := range environmentWithoutAuthentication() {
		name, _, _ := strings.Cut(value, "=")
		if name != "MP_LOCAL_AUTH" && name != "MP_LOCAL_ADMIN_PASSWORD" && name != "MESSAGEPIT_TEST_LOCAL_ENV" {
			base = append(base, value)
		}
	}
	for _, value := range []string{"", "enabled", "  "} {
		command := exec.Command(os.Args[0], "-test.run=^$")
		command.Env = append(append([]string{}, base...), "MP_LOCAL_AUTH="+value)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "MP_LOCAL_AUTH") {
			t.Fatal("invalid local authentication setting did not fail closed")
		}
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLocalAuthEnvironmentIsFailClosed$")
	command.Env = append(base, "MP_LOCAL_AUTH=true", "MP_LOCAL_ADMIN_PASSWORD=test-bootstrap-password", "MESSAGEPIT_TEST_LOCAL_ENV=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("valid local authentication environment failed: %v %s", err, output)
	}
}
