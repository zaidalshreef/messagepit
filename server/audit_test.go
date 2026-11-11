package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestAuditRecordsActionsWithoutSecretsAndIsAdministratorOnly(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	router := apiRoutes()
	admin := oidcauth.Principal{Issuer: "https://id.example", Subject: "admin", Admin: true}
	viewer := oidcauth.Principal{Issuer: "https://id.example", Subject: "viewer"}
	request := func(method, path, body string, principal oidcauth.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodPost, "/api/v1/groups", `{"name":"private-body-value"}`, admin); w.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/groups", `{"name":"other-private-value"}`, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("viewer created a group: %d", w.Code)
	}
	group, err := storage.CreateGroup(ctx, "Audit test")
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
	issued := request(http.MethodPost, "/api/v1/environments/"+envs[0].ID+"/tokens", `{"name":"CI secret name","role":"developer"}`, admin)
	var token struct {
		Secret string `json:"secret"`
	}
	if issued.Code != http.StatusCreated || json.Unmarshal(issued.Body.Bytes(), &token) != nil || token.Secret == "" {
		t.Fatalf("issue token: %d %s", issued.Code, issued.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/audit", "", viewer); w.Code != http.StatusForbidden {
		t.Fatalf("non-administrator accessed audit events: %d", w.Code)
	}
	w := request(http.MethodGet, "/api/v1/audit", "", admin)
	var body struct {
		Events []storage.AuditEvent `json:"events"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Events) != 3 {
		t.Fatalf("audit events: %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{token.Secret, "private-body-value", "other-private-value", "CI secret name"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("audit exposed request data: %s", secret)
		}
	}
	if body.Events[0].Action != "environment.tokens.post" || body.Events[0].Subject != "admin" || body.Events[0].EnvironmentID != envs[0].ID {
		t.Fatalf("incorrect audit metadata: %+v", body.Events[0])
	}
	if body.Events[1].Status != http.StatusForbidden || body.Events[1].Subject != "viewer" {
		t.Fatalf("denied action not recorded: %+v", body.Events[1])
	}
}
