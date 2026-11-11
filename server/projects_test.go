package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestProjectAPIRequiresExplicitGrantsAndRoles(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob", "manager"} {
		if err := storage.AddGroupMember(ctx, group.ID, "https://id.example", subject); err != nil {
			t.Fatal(err)
		}
	}
	first, err := storage.CreateProject(ctx, group.ID, "Project A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := storage.CreateProject(ctx, group.ID, "Project B")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.GrantProjectRole(ctx, first.ID, "https://id.example", "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	if err := storage.GrantProjectRole(ctx, second.ID, "https://id.example", "bob", access.Developer); err != nil {
		t.Fatal(err)
	}
	if err := storage.GrantProjectRole(ctx, first.ID, "https://id.example", "manager", access.Admin); err != nil {
		t.Fatal(err)
	}

	router := apiRoutes()
	admin := oidcauth.Principal{Issuer: "https://id.example", Subject: "admin", Admin: true}
	alice := oidcauth.Principal{Issuer: "https://id.example", Subject: "alice"}
	bob := oidcauth.Principal{Issuer: "https://id.example", Subject: "bob"}
	manager := oidcauth.Principal{Issuer: "https://id.example", Subject: "manager"}
	request := func(method, path, body string, principal oidcauth.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	list := request(http.MethodGet, "/api/v1/groups/"+group.ID+"/projects", "", alice)
	var result struct {
		Projects []storage.Project `json:"projects"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &result) != nil || len(result.Projects) != 1 || result.Projects[0].ID != first.ID {
		t.Fatalf("Alice's projects: %d %s", list.Code, list.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/projects/"+second.ID, "", alice); w.Code != http.StatusNotFound {
		t.Fatalf("group membership exposed another project: %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/projects/"+first.ID+"/environments", `{"name":"staging"}`, alice); w.Code != http.StatusNotFound {
		t.Fatalf("viewer created an environment: %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/projects/"+second.ID+"/members", `{"subject":"alice","role":"admin"}`, bob); w.Code != http.StatusNotFound {
		t.Fatalf("developer granted a role: %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/projects/"+first.ID+"/members", `{"subject":"outsider","role":"viewer"}`, manager); w.Code != http.StatusBadRequest {
		t.Fatalf("non-group member received project grant: %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/projects/"+first.ID+"/members", `{"subject":"bob","role":"developer"}`, manager); w.Code != http.StatusNoContent {
		t.Fatalf("project administrator could not grant role: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/projects/"+first.ID+"/environments", `{"name":"staging"}`, manager); w.Code != http.StatusCreated {
		t.Fatalf("project administrator could not create environment: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodDelete, "/api/v1/groups/"+group.ID, "", admin); w.Code != http.StatusConflict {
		t.Fatalf("group containing projects was deleted: %d", w.Code)
	}
	if err := storage.RemoveGroupMember(ctx, group.ID, alice.Issuer, alice.Subject); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/projects/"+first.ID, "", alice); w.Code != http.StatusNotFound {
		t.Fatalf("removed group member retained project access: %d", w.Code)
	}
}
