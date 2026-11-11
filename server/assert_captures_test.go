package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestCaptureAssertionScriptScopesAndMatchesRecords(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to test the capture assertion script", tool)
		}
	}
	setup()
	defer storage.Close()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Assertions")
	if err != nil {
		t.Fatal(err)
	}
	makeEnvironment := func(name string) string {
		t.Helper()
		project, err := storage.CreateProject(ctx, group.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		envs, err := storage.ListProjectEnvironments(ctx, project.ID)
		if err != nil || len(envs) != 1 {
			t.Fatalf("environments: %+v, %v", envs, err)
		}
		return envs[0].ID
	}
	env := makeEnvironment("A")
	otherEnv := makeEnvironment("B")
	issued, secret, err := storage.IssueServiceToken(ctx, env, "CI viewer", access.Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const correlation = "test-assertion-flow"
	storeEmail := func(environment, subject, body string) {
		t.Helper()
		message := []byte("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: " + subject + "\r\nX-MessagePit-Correlation-ID: " + correlation + "\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n")
		if _, err := storage.StoreInEnvironment(ctx, environment, &message, nil); err != nil {
			t.Fatal(err)
		}
	}
	storeEmail(env, "Welcome aboard", "Your activation code is 123456")
	storeEmail(env, "Unrelated subject", "A different body")
	storeEmail(env, `Literal "$(printf executed)"`, "Literal content")
	html := []byte("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: Arabic HTML\r\nX-MessagePit-Correlation-ID: " + correlation + "\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n<h1>رمز التحقق</h1>\r\n")
	if _, err := storage.StoreInEnvironment(ctx, env, &html, nil); err != nil {
		t.Fatal(err)
	}
	storeEmail(otherEnv, "Private subject", "Private body")
	smsID, err := storage.StoreSMSInEnvironment(ctx, env, "+155501", "+155502", "Your SMS code is 654321", "account", correlation)
	if err != nil {
		t.Fatal(err)
	}
	hookID, err := storage.StoreWebhookInEnvironment(ctx, env, "POST", "/events/order", "", map[string][]string{storage.CorrelationHeader: {correlation}}, []byte(`{"status":"created"}`), "application/json", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	router := apiRoutes()
	pollStarted := make(chan struct{})
	absenceStarted := make(chan struct{})
	revocationStarted := make(chan struct{})
	var pollOnce, absenceOnce, revocationOnce sync.Once
	var pollCount atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		token, err := storage.AuthenticateServiceToken(r.Context(), value)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		principal := oidcauth.ServicePrincipal{ID: token.ID, EnvironmentID: token.EnvironmentID, Role: token.Role}
		if strings.HasSuffix(r.URL.Path, "/correlations/revoked-flow") {
			router.ServeHTTP(w, r.WithContext(oidcauth.WithServicePrincipal(r.Context(), principal)))
			revocationOnce.Do(func() { close(revocationStarted) })
			return
		}
		if strings.HasSuffix(r.URL.Path, "/correlations/forbidden-flow") {
			router.ServeHTTP(w, r.WithContext(oidcauth.WithServicePrincipal(r.Context(), principal)))
			absenceOnce.Do(func() { close(absenceStarted) })
			return
		}
		if strings.HasSuffix(r.URL.Path, "/correlations/delayed-flow") {
			router.ServeHTTP(w, r.WithContext(oidcauth.WithServicePrincipal(r.Context(), principal)))
			pollCount.Add(1)
			pollOnce.Do(func() { close(pollStarted) })
			return
		}
		router.ServeHTTP(w, r.WithContext(oidcauth.WithServicePrincipal(r.Context(), principal)))
	}))
	defer app.Close()

	run := func(extra map[string]string) (string, error) {
		t.Helper()
		values := map[string]string{
			"MESSAGEPIT_URL": app.URL, "MESSAGEPIT_ENVIRONMENT_ID": env, "MESSAGEPIT_TOKEN": secret,
			"MESSAGEPIT_CORRELATION_ID": correlation, "MESSAGEPIT_WAIT_SECONDS": "0",
		}
		for key, value := range extra {
			values[key] = value
		}
		command := exec.Command("bash", "../scripts/assert-captures.sh")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "MESSAGEPIT_") {
				command.Env = append(command.Env, value)
			}
		}
		for key, value := range values {
			command.Env = append(command.Env, key+"="+value)
		}
		out, err := command.CombinedOutput()
		if strings.Contains(string(out), secret) {
			t.Fatal("assertion output exposed the service credential")
		}
		return string(out), err
	}

	if out, err := run(nil); err != nil {
		t.Fatalf("existing count-only assertions: %s: %v", out, err)
	}
	matching := map[string]string{
		"MESSAGEPIT_EXPECT_EMAILS": "1", "MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Welcome",
		"MESSAGEPIT_EXPECT_EMAIL_BODY": "activation code", "MESSAGEPIT_EXPECT_EMAIL_TO": "RECIPIENT@example.test",
		"MESSAGEPIT_EXPECT_SMS_BODY": "654321", "MESSAGEPIT_EXPECT_SMS_TO": "+155502",
		"MESSAGEPIT_EXPECT_WEBHOOK_METHOD": "post", "MESSAGEPIT_EXPECT_WEBHOOK_PATH": "/events/order",
		"MESSAGEPIT_EXPECT_WEBHOOK_BODY": `"created"`,
	}
	if out, err := run(matching); err != nil {
		t.Fatalf("matching content assertions: %s: %v", out, err)
	} else if strings.Contains(out, "654321") || strings.Contains(out, "activation code") {
		t.Fatal("assertion output exposed message content")
	}
	emails, err := storage.ListEnvironmentMessages(env, 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range emails {
		if email.Read {
			t.Fatal("asserting a message marked it as read")
		}
	}
	sms, err := storage.GetSMS(smsID)
	if err != nil || sms.Read {
		t.Fatalf("asserting an SMS changed its read status: %+v %v", sms, err)
	}
	hook, err := storage.GetWebhook(hookID)
	if err != nil || hook.Read {
		t.Fatalf("asserting a webhook changed its read status: %+v %v", hook, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Arabic HTML", "MESSAGEPIT_EXPECT_EMAIL_BODY": "رمز التحقق"}); err != nil {
		t.Fatalf("HTML and Unicode content assertion: %s %v", out, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAIL_SUBJECT": `"$(printf executed)"`}); err != nil {
		t.Fatalf("literal shell characters in expectations: %s %v", out, err)
	}
	localhostURL := strings.Replace(app.URL, "127.0.0.1", "localhost", 1)
	if out, err := run(map[string]string{"MESSAGEPIT_URL": localhostURL, "http_proxy": "http://127.0.0.1:1", "NO_PROXY": "", "no_proxy": ""}); err != nil {
		t.Fatalf("local test did not bypass the proxy: %s %v", out, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Welcome", "MESSAGEPIT_EXPECT_EMAIL_BODY": "different body"}); err == nil || !strings.Contains(out, "Timed out") || !strings.Contains(out, "Unmatched channels: email") {
		t.Fatalf("different records satisfied a combined assertion: %s %v", out, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAILS": "2", "MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Welcome"}); err == nil || !strings.Contains(out, "Timed out") {
		t.Fatalf("matching count included a nonmatching message: %s %v", out, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Private subject"}); err == nil || !strings.Contains(out, "Timed out") {
		t.Fatalf("assertion inspected another project's message: %s %v", out, err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_ENVIRONMENT_ID": otherEnv}); err == nil || !strings.Contains(out, "Unable to query") {
		t.Fatalf("viewer credential accessed another environment: %s %v", out, err)
	}
	for _, url := range []string{"http://example.test", "http://127.0.0.1:80@example.test", "https://user@example.test"} {
		if out, err := run(map[string]string{"MESSAGEPIT_URL": url}); err == nil || !strings.Contains(out, "must use HTTPS") {
			t.Fatalf("unsafe credential URL %q was accepted: %s %v", url, out, err)
		}
	}

	inserted := make(chan error, 1)
	go func() {
		select {
		case <-pollStarted:
		case <-time.After(5 * time.Second):
			inserted <- errors.New("assertion did not start polling")
			return
		}
		time.Sleep(250 * time.Millisecond)
		_, err := storage.StoreSMSInEnvironment(ctx, env, "+155501", "+155502", "Arrived later", "account", "delayed-flow")
		inserted <- err
	}()
	out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAILS": "0", "MESSAGEPIT_EXPECT_SMS_BODY": "Arrived later", "MESSAGEPIT_CORRELATION_ID": "delayed-flow", "MESSAGEPIT_WAIT_SECONDS": "4"})
	if insertErr := <-inserted; insertErr != nil {
		t.Fatal(insertErr)
	}
	if err != nil {
		t.Fatalf("assertion did not wait for a matching capture: %s %v", out, err)
	}
	if pollCount.Load() < 2 {
		t.Fatal("assertion did not retry after the initial empty result")
	}
	for _, tc := range []struct {
		flag, label string
	}{
		{"MESSAGEPIT_EXPECT_NO_EMAILS", "email"},
		{"MESSAGEPIT_EXPECT_NO_SMS", "sms"},
		{"MESSAGEPIT_EXPECT_NO_WEBHOOKS", "webhooks"},
	} {
		if out, err := run(map[string]string{tc.flag: "1"}); err == nil || !strings.Contains(out, "Unexpected captures in forbidden channels: "+tc.label) {
			t.Fatalf("existing %s did not fail an absence check: %s %v", tc.label, out, err)
		}
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_NO_EMAILS": "1", "MESSAGEPIT_CORRELATION_ID": "empty-flow"}); err != nil {
		t.Fatalf("absence check did not override the default email minimum: %s %v", out, err)
	}
	for _, options := range []map[string]string{
		{"MESSAGEPIT_EXPECT_NO_EMAILS": "1", "MESSAGEPIT_EXPECT_EMAILS": "1"},
		{"MESSAGEPIT_EXPECT_NO_EMAILS": "1", "MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Welcome"},
		{"MESSAGEPIT_EXPECT_NO_SMS": "1", "MESSAGEPIT_EXPECT_SMS_BODY": "code"},
		{"MESSAGEPIT_EXPECT_NO_WEBHOOKS": "2"},
	} {
		out, err := run(options)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("conflicting or invalid absence expectations were accepted: %s %v", out, err)
		}
	}
	if _, err := storage.StoreSMSInEnvironment(ctx, otherEnv, "+155501", "+155502", "Other environment", "account", "isolated-flow"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAILS": "0", "MESSAGEPIT_EXPECT_NO_SMS": "1", "MESSAGEPIT_CORRELATION_ID": "isolated-flow"}); err != nil {
		t.Fatalf("absence check included another environment: %s %v", out, err)
	}
	emailOnly := []byte("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: Email only\r\nX-MessagePit-Correlation-ID: email-only-flow\r\n\r\nNo SMS should be sent.\r\n")
	if _, err := storage.StoreInEnvironment(ctx, env, &emailOnly, nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_EMAIL_SUBJECT": "Email only", "MESSAGEPIT_EXPECT_NO_SMS": "1", "MESSAGEPIT_CORRELATION_ID": "email-only-flow", "MESSAGEPIT_WAIT_SECONDS": "1"}); err != nil {
		t.Fatalf("email with no SMS did not pass: %s %v", out, err)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("absence check ended before its full window: %s", elapsed)
	}

	forbiddenInserted := make(chan error, 1)
	go func() {
		select {
		case <-absenceStarted:
		case <-time.After(5 * time.Second):
			forbiddenInserted <- errors.New("absence check did not start polling")
			return
		}
		time.Sleep(250 * time.Millisecond)
		_, err := storage.StoreSMSInEnvironment(ctx, env, "+155501", "+155502", "Must not be sent", "account", "forbidden-flow")
		forbiddenInserted <- err
	}()
	out, err = run(map[string]string{"MESSAGEPIT_EXPECT_EMAILS": "0", "MESSAGEPIT_EXPECT_NO_SMS": "1", "MESSAGEPIT_CORRELATION_ID": "forbidden-flow", "MESSAGEPIT_WAIT_SECONDS": "3"})
	if insertErr := <-forbiddenInserted; insertErr != nil {
		t.Fatal(insertErr)
	}
	if err == nil || !strings.Contains(out, "Unexpected captures in forbidden channels: sms") || strings.Contains(out, "Must not be sent") {
		t.Fatalf("late forbidden SMS was not reported safely: %s %v", out, err)
	}

	for range 100 {
		if _, err := storage.StoreSMSInEnvironment(ctx, env, "+155501", "+155502", "Capped result", "account", "capped-flow"); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run(map[string]string{"MESSAGEPIT_EXPECT_NO_EMAILS": "1", "MESSAGEPIT_CORRELATION_ID": "capped-flow"}); err == nil || !strings.Contains(out, "100-record correlation limit") {
		t.Fatalf("a truncated response was used to claim absence: %s %v", out, err)
	}
	revoked := make(chan error, 1)
	go func() {
		select {
		case <-revocationStarted:
		case <-time.After(5 * time.Second):
			revoked <- errors.New("revocation check did not start polling")
			return
		}
		time.Sleep(250 * time.Millisecond)
		revoked <- storage.RevokeServiceToken(ctx, env, issued.ID)
	}()
	out, err = run(map[string]string{"MESSAGEPIT_EXPECT_EMAILS": "0", "MESSAGEPIT_EXPECT_NO_SMS": "1", "MESSAGEPIT_CORRELATION_ID": "revoked-flow", "MESSAGEPIT_WAIT_SECONDS": "3"})
	if revokeErr := <-revoked; revokeErr != nil {
		t.Fatal(revokeErr)
	}
	if err == nil || !strings.Contains(out, "Unable to query") {
		t.Fatalf("an absence check succeeded after credential revocation: %s %v", out, err)
	}
	if out, err := run(nil); err == nil || !strings.Contains(out, "Unable to query") {
		t.Fatalf("revoked credential accessed captures: %s %v", out, err)
	}
}
