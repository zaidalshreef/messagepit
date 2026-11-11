package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/sendgrid"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestLocalSignInAndProjectIsolation(t *testing.T) {
	oldLocal, oldURL, oldAdmin, oldPassword := config.LocalAuth, config.LocalPublicURL, config.LocalAdminUsername, config.LocalAdminPassword
	oldOIDC, oldDB, oldTenant, oldManager := config.OIDC, config.Database, config.TenantID, oidcManager
	t.Cleanup(func() {
		config.LocalAuth, config.LocalPublicURL, config.LocalAdminUsername, config.LocalAdminPassword = oldLocal, oldURL, oldAdmin, oldPassword
		config.OIDC, config.Database, config.TenantID, oidcManager = oldOIDC, oldDB, oldTenant, oldManager
	})
	config.LocalAuth, config.LocalAdminUsername, config.LocalAdminPassword = true, "admin", "admin-password-for-test"
	config.OIDC, config.Database, config.TenantID = oidcauth.Config{}, filepath.Join(t.TempDir(), "local.db"), ""
	if err := storage.InitDB(); err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	app := httptest.NewUnstartedServer(nil)
	config.LocalPublicURL = "http://" + app.Listener.Addr().String()
	if err := InitSharedAuth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if config.LocalAdminPassword != "" {
		t.Fatal("bootstrap password remained in configuration")
	}
	app.Config.Handler = oidcManager.Wrap(apiRoutes())
	app.Start()
	defer app.Close()
	newClient := func() *http.Client {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	call := func(client *http.Client, method, path, body, contentType string) (int, string) {
		t.Helper()
		r, err := http.NewRequest(method, app.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Origin", app.URL)
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
	login := func(client *http.Client, username, password string) int {
		t.Helper()
		form := url.Values{"username": {username}, "password": {password}, "return_to": {"/groups"}}
		status, _ := call(client, http.MethodPost, "/auth/login", form.Encode(), "application/x-www-form-urlencoded")
		return status
	}
	admin, alice := newClient(), newClient()
	if status, _ := call(alice, http.MethodGet, "/api/v1/me", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous local access returned %d", status)
	}
	if login(admin, "admin", "admin-password-for-test") != http.StatusSeeOther {
		t.Fatal("local administrator could not sign in")
	}
	if status, _ := call(admin, http.MethodPost, "/api/v1/local-users", `{"username":"alice","password":"alice-password-for-test"}`, "application/json"); status != http.StatusCreated {
		t.Fatalf("local user creation returned %d", status)
	}
	if status, body := call(admin, http.MethodGet, "/api/v1/local-users", "", ""); status != http.StatusOK || strings.Contains(body, "alice-password-for-test") || strings.Contains(body, "PasswordHash") {
		t.Fatal("local user list was unavailable or exposed credentials")
	}
	if login(alice, "alice", "alice-password-for-test") != http.StatusSeeOther {
		t.Fatal("local user could not sign in")
	}
	if status, _ := call(alice, http.MethodGet, "/api/v1/local-users", "", ""); status != http.StatusForbidden {
		t.Fatalf("non-administrator accessed user management: %d", status)
	}
	status, body := call(admin, http.MethodPost, "/api/v1/groups", `{"name":"Local team"}`, "application/json")
	var group storage.Group
	if status != http.StatusCreated || json.Unmarshal([]byte(body), &group) != nil {
		t.Fatal("local administrator could not create a group")
	}
	if status, _ := call(admin, http.MethodPost, "/api/v1/groups/"+group.ID+"/members", `{"subject":"alice"}`, "application/json"); status != http.StatusNoContent {
		t.Fatal("local user could not be added to a group")
	}
	status, body = call(admin, http.MethodPost, "/api/v1/groups/"+group.ID+"/projects", `{"name":"Project A"}`, "application/json")
	var project storage.Project
	if status != http.StatusCreated || json.Unmarshal([]byte(body), &project) != nil {
		t.Fatal("local administrator could not create a project")
	}
	envs, err := storage.ListProjectEnvironments(context.Background(), project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatal("project environment was not created")
	}
	base := "/api/v1/environments/" + envs[0].ID
	if status, _ := call(alice, http.MethodGet, base+"/messages", "", ""); status != http.StatusNotFound {
		t.Fatal("group membership implicitly granted project access")
	}
	if status, _ := call(admin, http.MethodPost, "/api/v1/projects/"+project.ID+"/members", `{"subject":"alice","role":"viewer"}`, "application/json"); status != http.StatusNoContent {
		t.Fatal("explicit project grant failed")
	}
	if status, _ := call(alice, http.MethodGet, base+"/messages", "", ""); status != http.StatusOK {
		t.Fatal("explicit project grant did not permit access")
	}
	status, body = call(admin, http.MethodPost, base+"/tokens", `{"name":"CI","role":"developer","ttl_seconds":3600}`, "application/json")
	var token struct {
		Secret string `json:"secret"`
	}
	if status != http.StatusCreated || json.Unmarshal([]byte(body), &token) != nil || token.Secret == "" {
		t.Fatal("local administrator could not issue a scoped credential")
	}
	mail := `{"from":{"email":"sender@example.test"},"subject":"Local capture","personalizations":[{"to":[{"email":"recipient@example.test"}]}],"content":[{"type":"text/plain","value":"test"}]}`
	ingest := func(secret string) int {
		r := httptest.NewRequest(http.MethodPost, "/v3/mail/send", strings.NewReader(mail))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		sendgrid.CreateMessage(w, r)
		return w.Code
	}
	if ingest("") != http.StatusUnauthorized || ingest(token.Secret) != http.StatusAccepted {
		t.Fatal("local shared mode did not protect or accept scoped capture")
	}
	if status, body := call(alice, http.MethodGet, base+"/messages", "", ""); status != http.StatusOK || !strings.Contains(body, "Local capture") {
		t.Fatal("local user could not inspect an authorized capture")
	}
	if status, _ := call(alice, http.MethodPost, "/api/v1/local-password", `{"current_password":"alice-password-for-test","new_password":"alice-new-password-for-test"}`, "application/json"); status != http.StatusNoContent {
		t.Fatal("local user could not change their password")
	}
	if status, _ := call(alice, http.MethodGet, "/api/v1/me", "", ""); status != http.StatusUnauthorized {
		t.Fatal("password change did not invalidate the old session")
	}
	if login(alice, "alice", "alice-password-for-test") != http.StatusUnauthorized || login(alice, "alice", "alice-new-password-for-test") != http.StatusSeeOther {
		t.Fatal("password change did not update sign-in credentials")
	}
	if status, _ := call(admin, http.MethodPost, "/api/v1/local-users/alice/password", `{"password":"alice-reset-password-for-test"}`, "application/json"); status != http.StatusNoContent {
		t.Fatal("administrator could not reset a user's password")
	}
	if status, _ := call(alice, http.MethodGet, "/api/v1/me", "", ""); status != http.StatusUnauthorized {
		t.Fatal("administrator password reset did not invalidate the user's session")
	}
	if login(alice, "alice", "alice-reset-password-for-test") != http.StatusSeeOther {
		t.Fatal("reset password did not permit sign-in")
	}
	if status, _ := call(admin, http.MethodDelete, "/api/v1/local-users/alice", "", ""); status != http.StatusNoContent {
		t.Fatal("administrator could not disable a local user")
	}
	if status, _ := call(alice, http.MethodGet, "/api/v1/me", "", ""); status != http.StatusUnauthorized {
		t.Fatal("disabled local user retained an active session")
	}
	if login(alice, "alice", "alice-reset-password-for-test") != http.StatusUnauthorized {
		t.Fatal("disabled local user could sign in")
	}
}
