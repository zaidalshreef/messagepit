package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestEnvironmentAPIScopesLegacyRecordsAndAttachments(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	issuer := "https://id.example"
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, storage.LegacyGroupID, issuer, subject); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.GrantProjectRole(ctx, storage.LegacyProjectID, issuer, "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	group, err := storage.CreateGroup(ctx, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AddGroupMember(ctx, group.ID, issuer, "alice"); err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.GrantProjectRole(ctx, project.ID, issuer, "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	environments, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(environments) != 1 {
		t.Fatalf("other environment: %+v %v", environments, err)
	}
	email, err := os.ReadFile("../internal/storage/testdata/regular-attachment.eml")
	if err != nil {
		t.Fatal(err)
	}
	emailID, err := storage.Store(&email, nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := storage.ReadMessage(emailID)
	if err != nil || len(message.Attachments) == 0 {
		t.Fatalf("test email attachment: %+v %v", message, err)
	}
	smsID, err := storage.StoreSMS("sender", "recipient", "Your verification code is 135790", "")
	if err != nil {
		t.Fatal(err)
	}
	hookID, err := storage.StoreWebhook("POST", "/legacy", "", nil, []byte("legacy webhook"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}

	router := apiRoutes()
	alice := oidcauth.Principal{Issuer: issuer, Subject: "alice"}
	bob := oidcauth.Principal{Issuer: issuer, Subject: "bob"}
	request := func(path string, principal oidcauth.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/environments/" + storage.LegacyEnvironmentID
	for _, path := range []string{
		"/messages/" + emailID, "/messages/" + emailID + "/raw", "/messages/" + emailID + "/headers", "/messages/" + emailID + "/html-check", "/messages/" + emailID + "/simulation",
		"/messages/" + emailID + "/part/" + message.Attachments[0].PartID,
		"/sms/" + smsID, "/sms/" + smsID + "/simulation", "/webhooks/" + hookID,
	} {
		if w := request(base+path, alice); w.Code != http.StatusOK {
			t.Errorf("authorized %s returned %d: %s", path, w.Code, w.Body.String())
		}
		if w := request(base+path, bob); w.Code != http.StatusNotFound {
			t.Errorf("ungranted user accessed %s: %d", path, w.Code)
		}
	}
	for _, kind := range []string{"messages", "sms", "webhooks"} {
		w := request(base+"/"+kind, alice)
		var result struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Messages) != 1 {
			t.Errorf("authorized %s list: %d %s", kind, w.Code, w.Body.String())
		}
	}
	other := "/api/v1/environments/" + environments[0].ID + "/messages/" + emailID
	if w := request(other, alice); w.Code != http.StatusNotFound {
		t.Fatalf("legacy message exposed in another authorized environment: %d", w.Code)
	}
	otpPath := base + "/sms/" + smsID + "/otp"
	if w := request(otpPath, alice); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Body.String() != "{\"code\":\"135790\"}\n" {
		t.Fatalf("authorized verification code: %d %s", w.Code, w.Body.String())
	}
	if w := request(otpPath, bob); w.Code != http.StatusNotFound {
		t.Fatalf("ungranted user accessed verification code: %d", w.Code)
	}
	if w := request("/api/v1/environments/"+environments[0].ID+"/sms/"+smsID+"/otp", alice); w.Code != http.StatusNotFound {
		t.Fatalf("verification code exposed in another environment: %d", w.Code)
	}
	ambiguous, err := storage.StoreSMS("sender", "recipient", "OTP 123456 and OTP 987654", "")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(base+"/sms/"+ambiguous+"/otp", alice); w.Code != http.StatusConflict {
		t.Fatalf("ambiguous verification code returned %d", w.Code)
	}
	failedSMS, err := storage.StoreSMSInEnvironment(ctx, environments[0].ID, "sender", "+15005550010", "failure test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	failurePath := "/api/v1/environments/" + environments[0].ID + "/sms/" + failedSMS + "/simulation"
	if w := request(failurePath, alice); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"failed"`) || !strings.Contains(w.Body.String(), `"error_code":30008`) {
		t.Fatalf("failure simulation: %d %s", w.Code, w.Body.String())
	}
	if w := request(failurePath, bob); w.Code != http.StatusNotFound {
		t.Fatalf("ungranted user accessed failure simulation: %d", w.Code)
	}
	listed, err := storage.List(0, 0, 50)
	if err != nil || len(listed) != 1 || listed[0].Read {
		t.Fatalf("read-only project view changed message read status: %+v %v", listed, err)
	}
}

func TestScopedLatestShortcutCannotReadAnotherEnvironment(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	content, err := os.ReadFile("../internal/storage/testdata/regular-attachment.eml")
	if err != nil {
		t.Fatal(err)
	}
	id, err := storage.Store(&content, nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := storage.ReadMessage(id)
	if err != nil || len(message.Attachments) == 0 {
		t.Fatalf("test attachment: %+v %v", message, err)
	}
	database, err := sql.Open("sqlite", config.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, table := range []string{"mailbox", "mailbox_data"} {
		if _, err := database.Exec(fmt.Sprintf(`UPDATE %s%s SET ID = 'latest' WHERE ID = ?`, config.TenantID, table), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(fmt.Sprintf(`UPDATE %smailbox SET Created = 1 WHERE ID = 'latest'`, config.TenantID)); err != nil {
		t.Fatal(err)
	}
	group, err := storage.CreateGroup(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environment: %+v %v", envs, err)
	}
	if _, err := storage.StoreInEnvironment(ctx, envs[0].ID, &content, nil); err != nil {
		t.Fatal(err)
	}
	router := apiRoutes()
	for _, suffix := range []string{"", "/raw", "/headers", "/html-check", "/part/" + message.Attachments[0].PartID} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/environments/"+storage.LegacyEnvironmentID+"/messages/latest"+suffix, nil)
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), oidcauth.Principal{Issuer: "https://id.example", Subject: "admin", Admin: true}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("scoped latest%s returned %d", suffix, w.Code)
		}
	}
}
