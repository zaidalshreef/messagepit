package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/sendgrid"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/coreydaley/messagepit/internal/twilio"
	"github.com/coreydaley/messagepit/internal/webhookd"
	"github.com/gorilla/mux"
)

func TestScopedProviderIngestRequiresWriteCredential(t *testing.T) {
	setup()
	defer storage.Close()
	previous := config.OIDC
	config.OIDC = oidcauth.Config{Issuer: "https://id.example"}
	defer func() { config.OIDC = previous }()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Capture")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Project A")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	env := envs[0].ID
	issued, secret, err := storage.IssueServiceToken(ctx, env, "CI", access.Developer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, viewer, err := storage.IssueServiceToken(ctx, env, "Reader", access.Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mail := `{"from":{"email":"from@example.com"},"subject":"Scoped email","personalizations":[{"to":[{"email":"to@example.com"}]}],"content":[{"type":"text/plain","value":"message"}]}`
	sms := url.Values{"From": {"+15550000001"}, "To": {"+15550000002"}, "Body": {"Scoped SMS"}}.Encode()
	type adapter struct {
		name, path, body, contentType string
		handler                       http.HandlerFunc
		basic                         bool
		want                          int
	}
	adapters := []adapter{
		{"sendgrid", "/v3/mail/send", mail, "application/json", sendgrid.CreateMessage, false, http.StatusAccepted},
		{"twilio", "/2010-04-01/Accounts/ACtest/Messages.json", sms, "application/x-www-form-urlencoded", twilio.CreateMessage, true, http.StatusCreated},
		{"webhook", "/capture", "Scoped webhook", "text/plain", webhookd.CaptureHandler, false, http.StatusOK},
	}
	call := func(a adapter, credential, remote string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, a.path, strings.NewReader(a.body))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", a.contentType)
		r.Header.Set(storage.CorrelationHeader, "workflow-123")
		if a.basic {
			r.SetBasicAuth("ACtest", credential)
			r = mux.SetURLVars(r, map[string]string{"AccountSid": "ACtest"})
		} else if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		a.handler(w, r)
		return w
	}
	for _, a := range adapters {
		for _, credential := range []string{"", "invalid", viewer} {
			if w := call(a, credential, "127.0.0.1:1234"); w.Code != http.StatusUnauthorized {
				t.Errorf("%s accepted non-writer: %d", a.name, w.Code)
			}
		}
		if w := call(a, secret, "192.0.2.10:1234"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s accepted plaintext remote credential: %d", a.name, w.Code)
		}
		if w := call(a, secret, "127.0.0.1:1234"); w.Code != a.want {
			t.Errorf("%s scoped capture failed: %d %s", a.name, w.Code, w.Body.String())
		}
	}
	tooLarge := httptest.NewRequest(http.MethodPost, "/capture", strings.NewReader(strings.Repeat("x", (10<<20)+1)))
	tooLarge.RemoteAddr = "127.0.0.1:1234"
	tooLarge.Header.Set("Authorization", "Bearer "+secret)
	tooLargeResponse := httptest.NewRecorder()
	webhookd.CaptureHandler(tooLargeResponse, tooLarge)
	if tooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized webhook was accepted: %d", tooLargeResponse.Code)
	}
	emails, e1 := storage.ListEnvironmentMessages(env, 0, 0, 10)
	smsMessages, e2 := storage.ListEnvironmentSMS(env, 0, 10)
	hooks, e3 := storage.ListEnvironmentWebhooks(env, 0, 10)
	if e1 != nil || e2 != nil || e3 != nil || len(emails) != 1 || len(smsMessages) != 1 || len(hooks) != 1 {
		t.Fatalf("captured records: email=%+v sms=%+v webhook=%+v errors=%v %v %v", emails, smsMessages, hooks, e1, e2, e3)
	}
	legacyMail, _ := storage.ListEnvironmentMessages(storage.LegacyEnvironmentID, 0, 0, 10)
	legacySMS, _ := storage.ListEnvironmentSMS(storage.LegacyEnvironmentID, 0, 10)
	legacyHooks, _ := storage.ListEnvironmentWebhooks(storage.LegacyEnvironmentID, 0, 10)
	if len(legacyMail)+len(legacySMS)+len(legacyHooks) != 0 {
		t.Fatal("scoped capture appeared in the legacy environment")
	}
	records, err := storage.ListCorrelatedRecords(ctx, env, "workflow-123")
	if err != nil || len(records) != 3 {
		t.Fatalf("provider correlation: %+v %v", records, err)
	}
	legacyRecords, err := storage.ListCorrelatedRecords(ctx, storage.LegacyEnvironmentID, "workflow-123")
	if err != nil || len(legacyRecords) != 0 {
		t.Fatalf("correlation crossed environments: %+v %v", legacyRecords, err)
	}
	service := oidcauth.ServicePrincipal{ID: issued.ID, EnvironmentID: env, Role: access.Developer}
	correlationRequest := func(environmentID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+environmentID+"/correlations/workflow-123", nil)
		r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), service))
		w := httptest.NewRecorder()
		apiRoutes().ServeHTTP(w, r)
		return w
	}
	var found struct {
		Records []storage.CorrelatedRecord `json:"records"`
	}
	if w := correlationRequest(env); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &found) != nil || len(found.Records) != 3 {
		t.Fatalf("correlation API: %d %s", w.Code, w.Body.String())
	}
	if w := correlationRequest(storage.LegacyEnvironmentID); w.Code != http.StatusNotFound {
		t.Fatalf("service token searched another environment: %d", w.Code)
	}
	searchRequest := func(environmentID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+environmentID+"/search?query=Scoped", nil)
		r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), service))
		w := httptest.NewRecorder()
		apiRoutes().ServeHTTP(w, r)
		return w
	}
	var search struct {
		Email struct {
			Total int `json:"total"`
		} `json:"email"`
		SMS struct {
			Total int `json:"total"`
		} `json:"sms"`
		Webhooks struct {
			Total int `json:"total"`
		} `json:"webhooks"`
	}
	if w := searchRequest(env); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &search) != nil || search.Email.Total != 1 || search.SMS.Total != 1 || search.Webhooks.Total != 1 {
		t.Fatalf("cross-channel search: %d %s", w.Code, w.Body.String())
	}
	if w := searchRequest(storage.LegacyEnvironmentID); w.Code != http.StatusNotFound {
		t.Fatalf("cross-channel search crossed environments: %d", w.Code)
	}
	hook, err := storage.GetWebhook(hooks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(hook)
	if strings.Contains(string(serialized), secret) || strings.Contains(string(serialized), "Authorization") {
		t.Fatalf("captured request exposed its credential: %s", serialized)
	}
	if err := storage.RevokeServiceToken(ctx, env, issued.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range adapters {
		if w := call(a, secret, "127.0.0.1:1234"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s accepted revoked credential: %d", a.name, w.Code)
		}
	}
}
