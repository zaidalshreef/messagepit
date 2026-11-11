package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestEnvironmentServiceTokens(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Token test")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Project")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	admin := oidcauth.Principal{Issuer: "https://id.example", Subject: "admin", Admin: true}
	router := apiRoutes()
	request := func(method, path, body string, principal oidcauth.Principal, service *oidcauth.ServicePrincipal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if service != nil {
			r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), *service))
		} else {
			r = r.WithContext(oidcauth.WithPrincipal(r.Context(), principal))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/environments/" + envs[0].ID
	issued := request(http.MethodPost, base+"/tokens", `{"name":"CI","role":"viewer","ttl_seconds":3600}`, admin, nil)
	var result struct {
		Token  storage.ServiceToken `json:"token"`
		Secret string               `json:"secret"`
	}
	if issued.Code != http.StatusCreated || json.Unmarshal(issued.Body.Bytes(), &result) != nil || result.Secret == "" || issued.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue token: %d %s", issued.Code, issued.Body.String())
	}
	listed := request(http.MethodGet, base+"/tokens", "", admin, nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), result.Secret) {
		t.Fatalf("token listing disclosed secret: %d %s", listed.Code, listed.Body.String())
	}
	token, err := storage.AuthenticateServiceToken(ctx, result.Secret)
	if err != nil {
		t.Fatal(err)
	}
	service := oidcauth.ServicePrincipal{EnvironmentID: token.EnvironmentID, Role: token.Role}
	if w := request(http.MethodGet, base+"/messages", "", admin, &service); w.Code != http.StatusOK {
		t.Fatalf("viewer could not read its environment: %d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/environments/"+storage.LegacyEnvironmentID+"/messages", "", admin, &service); w.Code != http.StatusNotFound {
		t.Fatalf("token read another environment: %d", w.Code)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := request(method, base+"/tokens", `{"name":"escalation","role":"developer"}`, admin, &service); w.Code != http.StatusNotFound {
			t.Fatalf("service token managed credentials: %d", w.Code)
		}
	}
	developer := oidcauth.ServicePrincipal{EnvironmentID: envs[0].ID, Role: access.Developer}
	ownedSMS, err := storage.StoreSMSInEnvironment(ctx, envs[0].ID, "a", "b", "scoped", "", "")
	if err != nil {
		t.Fatal(err)
	}
	legacySMS, err := storage.StoreSMS("a", "b", "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodDelete, base+"/sms/"+ownedSMS, "", admin, &service); w.Code != http.StatusNotFound {
		t.Fatalf("viewer deleted a record: %d", w.Code)
	}
	if w := request(http.MethodDelete, base+"/sms/"+legacySMS, "", admin, &developer); w.Code != http.StatusNotFound {
		t.Fatalf("developer deleted another environment's record: %d", w.Code)
	}
	if w := request(http.MethodDelete, base+"/sms/"+ownedSMS, "", admin, &developer); w.Code != http.StatusNoContent {
		t.Fatalf("developer could not delete an owned record: %d", w.Code)
	}
	if _, err := storage.GetSMS(legacySMS); err != nil {
		t.Fatalf("cross-environment deletion removed legacy SMS: %v", err)
	}
	if w := request(http.MethodDelete, base+"/tokens/"+result.Token.ID, "", admin, &developer); w.Code != http.StatusNotFound {
		t.Fatalf("developer token revoked credentials: %d", w.Code)
	}
	if w := request(http.MethodPut, base+"/retention", `{"max_age_seconds":3600}`, admin, &developer); w.Code != http.StatusNotFound {
		t.Fatalf("developer token changed retention: %d", w.Code)
	}
	if w := request(http.MethodPut, base+"/retention", `{}`, admin, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("missing retention value was accepted: %d", w.Code)
	}
	if w := request(http.MethodPut, base+"/retention", `{"max_age_seconds":3600}`, admin, nil); w.Code != http.StatusNoContent {
		t.Fatalf("administrator could not set retention: %d", w.Code)
	}
	if w := request(http.MethodGet, base+"/retention", "", admin, &service); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "3600") {
		t.Fatalf("viewer could not inspect retention: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodDelete, base+"/tokens/"+result.Token.ID, "", admin, nil); w.Code != http.StatusNoContent {
		t.Fatalf("administrator could not revoke token: %d", w.Code)
	}
	if _, err := storage.AuthenticateServiceToken(ctx, result.Secret); err == nil {
		t.Fatal("revoked credential still authenticates")
	}
	if w := request(http.MethodPost, base+"/tokens", `{"name":"bad","role":"admin"}`, admin, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("issued admin service token: %d", w.Code)
	}
}
