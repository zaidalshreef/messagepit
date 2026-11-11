package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestGroupsEnforceMembershipAndAdministratorAccess(t *testing.T) {
	setup()
	defer storage.Close()
	router := apiRoutes()
	admin := oidcauth.Principal{Issuer: "https://id.example", Subject: "admin", Admin: true}
	alice := oidcauth.Principal{Issuer: "https://id.example", Subject: "alice"}
	bob := oidcauth.Principal{Issuer: "https://id.example", Subject: "bob"}
	otherIssuer := oidcauth.Principal{Issuer: "https://other.example", Subject: "alice"}

	request := func(method, path, body string, principal *oidcauth.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if principal != nil {
			r = r.WithContext(oidcauth.WithPrincipal(r.Context(), *principal))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/api/v1/groups", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("standalone group API returned %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/groups", `{"name":"Unauthorized"}`, &alice); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin created a group: %d", w.Code)
	}
	create := func(name string) storage.Group {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": name})
		w := request(http.MethodPost, "/api/v1/groups", string(body), &admin)
		if w.Code != http.StatusCreated {
			t.Fatalf("create group: %d %s", w.Code, w.Body.String())
		}
		var group storage.Group
		if err := json.Unmarshal(w.Body.Bytes(), &group); err != nil {
			t.Fatal(err)
		}
		return group
	}
	first, second := create("Engineering"), create("QA")
	if w := request(http.MethodPost, "/api/v1/groups/"+first.ID+"/members", `{"subject":"alice"}`, &admin); w.Code != http.StatusNoContent {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/groups/"+second.ID+"/members", `{"subject":"bob"}`, &admin); w.Code != http.StatusNoContent {
		t.Fatalf("add member: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/groups/"+second.ID+"/members", `{"issuer":"https://other.example","subject":"alice"}`, &admin); w.Code != http.StatusBadRequest {
		t.Fatalf("member from another issuer was added: %d", w.Code)
	}

	list := func(principal oidcauth.Principal) []storage.Group {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/groups", "", &principal)
		if w.Code != http.StatusOK {
			t.Fatalf("list groups: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Groups []storage.Group `json:"groups"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Groups
	}
	if groups := list(admin); len(groups) != 3 {
		t.Fatalf("administrator saw %d groups", len(groups))
	}
	if groups := list(alice); len(groups) != 1 || groups[0].ID != first.ID {
		t.Fatalf("Alice saw incorrect groups: %+v", groups)
	}
	if groups := list(otherIssuer); len(groups) != 0 {
		t.Fatalf("identity from another issuer saw groups: %+v", groups)
	}
	if w := request(http.MethodGet, "/api/v1/groups/"+second.ID+"/members", "", &alice); w.Code != http.StatusNotFound {
		t.Fatalf("non-member could see another group's members: %d", w.Code)
	}
	if w := request(http.MethodGet, "/api/v1/groups/"+first.ID+"/members", "", &alice); w.Code != http.StatusOK {
		t.Fatalf("member could not list their group: %d", w.Code)
	}
	if w := request(http.MethodDelete, "/api/v1/groups/"+first.ID, "", &bob); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin deleted a group: %d", w.Code)
	}
	if w := request(http.MethodDelete, "/api/v1/groups/"+first.ID+"/members", `{"subject":"alice"}`, &admin); w.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d %s", w.Code, w.Body.String())
	}
	if groups := list(alice); len(groups) != 0 {
		t.Fatalf("removed member still sees group: %+v", groups)
	}
	if w := request(http.MethodDelete, "/api/v1/groups/"+first.ID, "", &admin); w.Code != http.StatusNoContent {
		t.Fatalf("delete group: %d %s", w.Code, w.Body.String())
	}
}
