package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/ai"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestAISummaryRequiresProjectAccessAndSendsOnlySelectedContent(t *testing.T) {
	setup()
	defer storage.Close()
	previous := config.AI
	defer func() { config.AI = previous }()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "selected-payload") || strings.Contains(string(body), "header-secret") || strings.Contains(string(body), "query-secret") {
			t.Errorf("AI received unexpected data: %s", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"QA summary"}}]}`))
	}))
	defer provider.Close()
	config.AI = ai.Config{BaseURL: provider.URL + "/v1", Model: "test-model"}
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "AI test")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Project")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob"} {
		if err := storage.AddGroupMember(ctx, group.ID, "https://id.example", subject); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.GrantProjectRole(ctx, project.ID, "https://id.example", "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	id, err := storage.StoreWebhookInEnvironment(ctx, envs[0].ID, "POST", "/example", "secret=query-secret", map[string][]string{"X-Secret": {"header-secret"}}, []byte("selected-payload"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	router := apiRoutes()
	request := func(env, subject string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/environments/"+env+"/webhooks/"+id+"/summary", nil)
		r = r.WithContext(oidcauth.WithPrincipal(r.Context(), oidcauth.Principal{Issuer: "https://id.example", Subject: subject}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(envs[0].ID, "bob"); w.Code != http.StatusNotFound {
		t.Fatalf("ungranted member requested a summary: %d", w.Code)
	}
	if w := request(storage.LegacyEnvironmentID, "alice"); w.Code != http.StatusNotFound || calls.Load() != 0 {
		t.Fatalf("cross-environment request reached AI: %d calls=%d", w.Code, calls.Load())
	}
	w := request(envs[0].ID, "alice")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "QA summary") || w.Header().Get("Cache-Control") != "no-store" || calls.Load() != 1 {
		t.Fatalf("authorized summary: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
}
