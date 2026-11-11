package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestCorrelatedVerificationCodesAreScopedAndUnambiguous(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Code tests")
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://id.example"
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, group.ID, issuer, subject); err != nil {
			t.Fatal(err)
		}
	}
	newEnvironment := func(name string) (storage.ProjectEnvironment, string) {
		t.Helper()
		project, err := storage.CreateProject(ctx, group.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		environments, err := storage.ListProjectEnvironments(ctx, project.ID)
		if err != nil || len(environments) != 1 {
			t.Fatalf("creating environment: %v", err)
		}
		return environments[0], project.ID
	}
	env, projectID := newEnvironment("Project A")
	other, _ := newEnvironment("Project B")
	if err := storage.GrantProjectRole(ctx, projectID, issuer, "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}

	storeEmail := func(environmentID, correlation, contentType, body string) string {
		t.Helper()
		message := []byte(fmt.Sprintf("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: Code test\r\nX-MessagePit-Correlation-ID: %s\r\nContent-Type: %s; charset=UTF-8\r\n\r\n%s\r\n", correlation, contentType, body))
		id, err := storage.StoreInEnvironment(ctx, environmentID, &message, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	storeSMS := func(environmentID, correlation, body string) string {
		t.Helper()
		id, err := storage.StoreSMSInEnvironment(ctx, environmentID, "+155501", "+155502", body, "test", correlation)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	emailID := storeEmail(env.ID, "test-run", "text/plain", "Your verification code is 005812.")
	smsID := storeSMS(env.ID, "test-run", "Your verification code is 654321.")
	storeEmail(env.ID, "test-run", "text/plain", "This message has no code.")
	storeEmail(other.ID, "test-run", "text/plain", "Your OTP is 999999.")
	storeSMS(other.ID, "test-run", "Your OTP is 999999.")
	storeSMS(env.ID, "other-run", "Your OTP is 999999.")
	storeEmail(env.ID, "html-run", "text/html", "<p>Your verification code is <strong>112233</strong></p>")

	router := apiRoutes()
	alice := oidcauth.Principal{Issuer: issuer, Subject: "alice"}
	bob := oidcauth.Principal{Issuer: issuer, Subject: "bob"}
	viewer := oidcauth.ServicePrincipal{ID: "viewer", EnvironmentID: env.ID, Role: access.Viewer}
	request := func(environmentID, correlation, kind string, principal *oidcauth.Principal, service *oidcauth.ServicePrincipal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+environmentID+"/correlations/"+correlation+"/otp?kind="+kind, nil)
		if principal != nil {
			r = r.WithContext(oidcauth.WithPrincipal(r.Context(), *principal))
		}
		if service != nil {
			r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), *service))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int, body string) {
		t.Helper()
		if w.Code != status || (body != "" && w.Body.String() != body) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("code lookup: got %d, cache %q, body %q", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
	}
	check(request(env.ID, "test-run", "messages", &alice, nil), http.StatusOK, "{\"code\":\"005812\"}\n")
	check(request(env.ID, "test-run", "sms", nil, &viewer), http.StatusOK, "{\"code\":\"654321\"}\n")
	check(request(env.ID, "html-run", "messages", &alice, nil), http.StatusOK, "{\"code\":\"112233\"}\n")
	check(request(env.ID, "test-run", "sms", &bob, nil), http.StatusNotFound, "")
	check(request(other.ID, "test-run", "sms", &alice, nil), http.StatusNotFound, "")
	check(request(other.ID, "test-run", "sms", nil, &viewer), http.StatusNotFound, "")
	check(request(env.ID, "missing-run", "sms", &alice, nil), http.StatusNotFound, "")
	check(request(env.ID, "test-run", "webhooks", &alice, nil), http.StatusBadRequest, "")
	check(request(env.ID, "test-run", "", &alice, nil), http.StatusBadRequest, "")
	check(request(env.ID, "bad%20id", "sms", &alice, nil), http.StatusBadRequest, "")

	// Exercise the documented CI flow through the real OIDC/service-token boundary.
	t.Run("CI viewer credentials", func(t *testing.T) {
		for _, tool := range []string{"bash", "curl", "jq"} {
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("%s is required to test the CI workflow", tool)
			}
		}
		var providerURL string
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/.well-known/openid-configuration" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["RS256"]}`, providerURL, providerURL+"/authorize", providerURL+"/token", providerURL+"/jwks")
		}))
		defer provider.Close()
		providerURL = provider.URL
		manager, err := oidcauth.New(ctx, oidcauth.Config{
			Issuer: providerURL, ClientID: "test-client", PublicURL: "http://127.0.0.1:8025", AllowedSubjects: "alice", Webroot: "/",
		})
		if err != nil {
			t.Fatal(err)
		}
		manager.SetTokenValidator(func(ctx context.Context, value string) (oidcauth.ServicePrincipal, error) {
			token, err := storage.AuthenticateServiceToken(ctx, value)
			return oidcauth.ServicePrincipal{ID: token.ID, EnvironmentID: token.EnvironmentID, Role: token.Role}, err
		})
		app := httptest.NewServer(manager.Wrap(router))
		defer app.Close()
		issued, secret, err := storage.IssueServiceToken(ctx, env.ID, "CI viewer", access.Viewer, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("bash", "-c", `
set -euo pipefail
set +x
MESSAGEPIT_EXPECT_EMAILS=0 MESSAGEPIT_EXPECT_SMS=1 \
  MESSAGEPIT_EXPECT_SMS_BODY='verification code' bash ../scripts/assert-captures.sh
api="${MESSAGEPIT_URL%/}/api/v1/environments/$MESSAGEPIT_ENVIRONMENT_ID"
code=$(printf 'header = "Authorization: Bearer %s"\n' "$MESSAGEPIT_TOKEN" |
  curl -q --config - --noproxy '*' --proto '=http,https' --fail --silent --show-error \
    --max-time 10 --url "$api/correlations/$MESSAGEPIT_CORRELATION_ID/otp?kind=sms" |
  jq -er '.code')
test "$code" = 654321
`)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "MESSAGEPIT_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env,
			"MESSAGEPIT_URL="+app.URL, "MESSAGEPIT_ENVIRONMENT_ID="+env.ID, "MESSAGEPIT_CORRELATION_ID=test-run",
			"MESSAGEPIT_TOKEN="+secret, "MESSAGEPIT_WAIT_SECONDS=0")
		output, err := command.CombinedOutput()
		if err != nil || strings.Contains(string(output), secret) || strings.Contains(string(output), "654321") {
			t.Fatal("CI capture assertion failed or exposed a credential or code")
		}
		requestWithToken := func() (int, string) {
			t.Helper()
			r, err := http.NewRequest(http.MethodGet, app.URL+"/api/v1/environments/"+env.ID+"/correlations/test-run/otp?kind=sms", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+secret)
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			return response.StatusCode, string(body)
		}
		if status, body := requestWithToken(); status != http.StatusOK || body != "{\"code\":\"654321\"}\n" {
			t.Fatal("CI viewer could not retrieve its correlated code")
		}
		if err := storage.RevokeServiceToken(ctx, env.ID, issued.ID); err != nil {
			t.Fatal(err)
		}
		if status, _ := requestWithToken(); status != http.StatusUnauthorized {
			t.Fatal("revoked viewer credential could still retrieve a code")
		}
	})

	storeEmail(env.ID, "test-run", "text/plain", "OTP: 005812")
	check(request(env.ID, "test-run", "messages", &alice, nil), http.StatusOK, "{\"code\":\"005812\"}\n")
	storeEmail(env.ID, "test-run", "text/plain", "OTP: 777777")
	ambiguous := request(env.ID, "test-run", "messages", &alice, nil)
	check(ambiguous, http.StatusConflict, "")
	if strings.Contains(ambiguous.Body.String(), "005812") || strings.Contains(ambiguous.Body.String(), "777777") {
		t.Fatal("ambiguous response revealed a verification code")
	}
	storeSMS(env.ID, "ambiguous-run", "OTP: 123456 and verification code: 987654")
	check(request(env.ID, "ambiguous-run", "sms", &alice, nil), http.StatusConflict, "")
	check(request(env.ID, "test-run", "sms", &alice, nil), http.StatusOK, "{\"code\":\"654321\"}\n")

	emails, err := storage.ListEnvironmentMessages(env.ID, 0, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range emails {
		if email.ID == emailID && email.Read {
			t.Fatal("correlated lookup marked an email as read")
		}
	}
	sms, err := storage.GetSMS(smsID)
	if err != nil || sms.Read {
		t.Fatal("correlated lookup changed the SMS read status")
	}

	for range 99 {
		storeSMS(env.ID, "limit-run", "OTP: 123456")
	}
	check(request(env.ID, "limit-run", "sms", &alice, nil), http.StatusOK, "{\"code\":\"123456\"}\n")
	storeSMS(env.ID, "limit-run", "no code")
	check(request(env.ID, "limit-run", "sms", &alice, nil), http.StatusConflict, "")
}
