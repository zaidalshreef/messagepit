package server

import (
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

func TestCorrelationTimelineRequiresExplicitProjectAccess(t *testing.T) {
	setup()
	defer storage.Close()
	ctx := context.Background()
	const issuer = "https://id.example"
	group, err := storage.CreateGroup(ctx, "Timeline API")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, group.ID, issuer, subject); err != nil {
			t.Fatal(err)
		}
	}
	project, err := storage.CreateProject(ctx, group.ID, "Timeline project")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.GrantProjectRole(ctx, project.ID, issuer, "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	environments, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(environments) != 1 {
		t.Fatal("timeline environment not created")
	}
	env := environments[0].ID
	for _, body := range []string{"first capture", "second capture", "third capture"} {
		if _, err := storage.StoreSMSInEnvironment(ctx, env, "sender", "receiver", body, "", "flow-42"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.StoreSMSInEnvironment(ctx, storage.LegacyEnvironmentID, "private", "private", "Other environment secret", "", "flow-42"); err != nil {
		t.Fatal(err)
	}
	router := apiRoutes()
	request := func(path, subject string, service *oidcauth.ServicePrincipal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if subject != "" {
			r = r.WithContext(oidcauth.WithPrincipal(r.Context(), oidcauth.Principal{Issuer: issuer, Subject: subject}))
		}
		if service != nil {
			r = r.WithContext(oidcauth.WithServicePrincipal(r.Context(), *service))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/environments/" + env + "/correlations/flow-42/timeline"
	viewer := oidcauth.ServicePrincipal{ID: "viewer", EnvironmentID: env, Role: access.Viewer}
	seen := make(map[string]bool)
	for _, query := range []string{"?limit=2", "?start=2&limit=2"} {
		w := request(base+query, "alice", nil)
		var result struct {
			Records []storage.CorrelationSummary `json:"records"`
			Total   int                          `json:"total"`
		}
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total != 3 || strings.Contains(w.Body.String(), "Other environment") {
			t.Fatalf("authorized timeline response: %d", w.Code)
		}
		for _, record := range result.Records {
			if seen[record.ID] {
				t.Fatal("record appeared on multiple pages")
			}
			seen[record.ID] = true
		}
	}
	if len(seen) != 3 || request(base, "", &viewer).Code != http.StatusOK {
		t.Fatal("timeline did not return all records to the authorized viewer")
	}
	if request(base, "bob", nil).Code != http.StatusNotFound || request(base, "", nil).Code != http.StatusNotFound {
		t.Fatal("group membership or an anonymous request exposed project data")
	}
	other := "/api/v1/environments/" + storage.LegacyEnvironmentID + "/correlations/flow-42/timeline"
	if request(other, "alice", nil).Code != http.StatusNotFound || request(other, "", &viewer).Code != http.StatusNotFound {
		t.Fatal("timeline crossed environments")
	}
	for _, query := range []string{"?start=-1", "?start=invalid", "?limit=0", "?limit=51", "?limit=invalid"} {
		if request(base+query, "alice", nil).Code != http.StatusBadRequest {
			t.Fatal("timeline accepted invalid pagination")
		}
	}
	if request("/api/v1/environments/"+env+"/correlations/bad%20id/timeline", "alice", nil).Code != http.StatusBadRequest {
		t.Fatal("timeline accepted an invalid correlation ID")
	}
}
