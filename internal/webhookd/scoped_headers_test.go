package webhookd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestScopedCaptureRedactsAuthorizationRegardlessOfCase(t *testing.T) {
	setup()
	defer storage.Close()
	previous := config.OIDC
	config.OIDC = oidcauth.Config{Issuer: "https://id.example"}
	defer func() { config.OIDC = previous }()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Webhook headers")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Headers")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environment: %+v %v", envs, err)
	}
	_, secret, err := storage.IssueServiceToken(ctx, envs[0].ID, "capture", access.Developer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/capture", strings.NewReader("payload"))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer "+secret)
	// Middleware can supply noncanonical keys even though net/http normally
	// canonicalizes headers read directly from a network connection.
	request.Header["authorization"] = []string{"Bearer " + secret}
	request.Header["aUtHoRiZaTiOn"] = []string{"alternate-private-value"}
	request.Header.Set("X-Trace", "visible-trace")
	response := httptest.NewRecorder()
	CaptureHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized capture returned %d", response.Code)
	}
	messages, err := storage.ListEnvironmentWebhooks(envs[0].ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("scoped captures: %+v %v", messages, err)
	}
	captured, err := storage.GetWebhook(messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for name := range captured.Headers {
		if strings.EqualFold(name, "Authorization") {
			t.Fatal("capture retained an authorization header")
		}
	}
	encoded, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "alternate-private-value") {
		t.Fatal("capture retained authorization credentials")
	}
	if len(captured.Headers["X-Trace"]) != 1 || captured.Headers["X-Trace"][0] != "visible-trace" {
		t.Fatal("capture lost an unrelated test header")
	}

	// Preserve the existing standalone behavior, where request headers are
	// captured for a user who has chosen not to enable shared authentication.
	config.OIDC = oidcauth.Config{}
	standalone := httptest.NewRequest(http.MethodPost, "/standalone", strings.NewReader("payload"))
	standalone.Header.Set("Authorization", "Bearer standalone-test-value")
	standaloneResponse := httptest.NewRecorder()
	CaptureHandler(standaloneResponse, standalone)
	if standaloneResponse.Code != http.StatusOK {
		t.Fatalf("standalone capture returned %d", standaloneResponse.Code)
	}
	all, err := storage.ListWebhooks(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range all {
		if item.Path != "/standalone" {
			continue
		}
		standaloneCapture, err := storage.GetWebhook(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(standaloneCapture.Headers["Authorization"]) != 1 || standaloneCapture.Headers["Authorization"][0] != "Bearer standalone-test-value" {
			t.Fatal("standalone capture changed authorization header behavior")
		}
		return
	}
	t.Fatal("standalone capture was not found")
}
