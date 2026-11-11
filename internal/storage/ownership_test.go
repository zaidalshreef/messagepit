package storage

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEnvironmentOwnershipKeepsLegacyAndProjectDataSeparate(t *testing.T) {
	setup("ownership_test")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "Project A")
	if err != nil {
		t.Fatal(err)
	}
	environments, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(environments) != 1 {
		t.Fatalf("project environment: %+v, %v", environments, err)
	}
	environmentID := environments[0].ID
	message, err := os.ReadFile("testdata/plain-text.eml")
	if err != nil {
		t.Fatal(err)
	}
	emailA, err := StoreInEnvironment(ctx, environmentID, &message, nil)
	if err != nil {
		t.Fatal(err)
	}
	emailLegacy, err := Store(&message, nil)
	if err != nil {
		t.Fatal(err)
	}
	smsA, err := StoreSMSInEnvironment(ctx, environmentID, "a", "b", "project", "", "")
	if err != nil {
		t.Fatal(err)
	}
	smsLegacy, err := StoreSMS("a", "b", "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	hookA, err := StoreWebhookInEnvironment(ctx, environmentID, "POST", "/project", "", nil, []byte("project"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	hookLegacy, err := StoreWebhook("POST", "/legacy", "", nil, []byte("legacy"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct{ kind, id string }{{"email", emailA}, {"sms", smsA}, {"webhook", hookA}} {
		if ok, err := RecordInEnvironment(ctx, record.kind, record.id, LegacyEnvironmentID); err != nil || ok {
			t.Fatalf("project record appeared in legacy environment: %s %v %v", record.kind, ok, err)
		}
		if ok, err := RecordInEnvironment(ctx, record.kind, record.id, environmentID); err != nil || !ok {
			t.Fatalf("project record missing from its environment: %s %v %v", record.kind, ok, err)
		}
	}
	for _, record := range []struct{ kind, id string }{{"email", emailLegacy}, {"sms", smsLegacy}, {"webhook", hookLegacy}} {
		if ok, err := RecordInEnvironment(ctx, record.kind, record.id, LegacyEnvironmentID); err != nil || !ok {
			t.Fatalf("legacy record missing: %s %v %v", record.kind, ok, err)
		}
		if ok, err := RecordInEnvironment(ctx, record.kind, record.id, environmentID); err != nil || ok {
			t.Fatalf("legacy record appeared in project: %s %v %v", record.kind, ok, err)
		}
	}
	if items, err := ListEnvironmentMessages(environmentID, 0, 0, 50); err != nil || len(items) != 1 || items[0].ID != emailA {
		t.Fatalf("project email list: %+v, %v", items, err)
	}
	if items, err := ListEnvironmentSMS(LegacyEnvironmentID, 0, 50); err != nil || len(items) != 1 || items[0].ID != smsLegacy {
		t.Fatalf("legacy SMS list: %+v, %v", items, err)
	}
	if items, err := ListEnvironmentWebhooks(environmentID, 0, 50); err != nil || len(items) != 1 || items[0].ID != hookA {
		t.Fatalf("project webhook list: %+v, %v", items, err)
	}
	if _, err := ListEnvironmentMessages("", 0, 0, 50); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("empty environment was accepted: %v", err)
	}
	if err := DeleteEnvironmentRecord(ctx, "email", emailA, LegacyEnvironmentID); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("cross-environment delete returned %v", err)
	}
	if err := DeleteEnvironmentRecord(ctx, "email", emailA, environmentID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteEnvironmentRecord(ctx, "sms", smsA, environmentID); err != nil {
		t.Fatal(err)
	}
	if err := DeleteEnvironmentRecord(ctx, "webhook", hookA, environmentID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tenant("message_ownership")).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted records retained ownership: %d %v", count, err)
	}
}

func TestEnvironmentSearchIsScopedAndPaginated(t *testing.T) {
	setup("ownership_search_test")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Search")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "Search project")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	env := envs[0].ID
	message := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: shared-marker\r\n\r\nsearch body\r\n")
	for i := 0; i < 2; i++ {
		if _, err := StoreInEnvironment(ctx, env, &message, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Store(&message, nil); err != nil {
		t.Fatal(err)
	}
	emails, total, err := SearchEnvironmentMessages(env, "subject:shared-marker", "", 1, 0, 1)
	if err != nil || total != 2 || len(emails) != 1 {
		t.Fatalf("scoped email search: %+v total=%d err=%v", emails, total, err)
	}
	if _, err := StoreSMSInEnvironment(ctx, env, "a", "b", "shared-marker", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreSMS("a", "b", "shared-marker", ""); err != nil {
		t.Fatal(err)
	}
	sms, total, err := SearchEnvironmentSMS(env, "shared-marker", 0, 10)
	if err != nil || total != 1 || len(sms) != 1 {
		t.Fatalf("scoped SMS search: %+v total=%d err=%v", sms, total, err)
	}
	body := []byte(strings.Repeat("x", 300) + "shared-marker")
	if _, err := StoreWebhookInEnvironment(ctx, env, "POST", "/", "", nil, body, "text/plain", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreWebhook("POST", "/", "", nil, body, "text/plain", ""); err != nil {
		t.Fatal(err)
	}
	hooks, total, err := SearchEnvironmentWebhooks(env, "shared-marker", 0, 10)
	if err != nil || total != 1 || len(hooks) != 1 {
		t.Fatalf("scoped full webhook body search: %+v total=%d err=%v", hooks, total, err)
	}
}

func TestScopedWritesRollBackWithoutEnvironment(t *testing.T) {
	setup("ownership_rollback_test")
	defer Close()
	ctx := context.Background()
	message, err := os.ReadFile("testdata/plain-text.eml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreInEnvironment(ctx, "missing", &message, nil); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("email write: %v", err)
	}
	if _, err := StoreSMSInEnvironment(ctx, "missing", "a", "b", "text", "", ""); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("SMS write: %v", err)
	}
	if _, err := StoreWebhookInEnvironment(ctx, "missing", "POST", "/", "", nil, nil, "", ""); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("webhook write: %v", err)
	}
	emails, e1 := List(0, 0, 10)
	sms, e2 := ListSMS(0, 10)
	hooks, e3 := ListWebhooks(0, 10)
	if e1 != nil || e2 != nil || e3 != nil || len(emails)+len(sms)+len(hooks) != 0 {
		t.Fatalf("failed scoped writes left records behind: %v %v %v", emails, sms, hooks)
	}
}
