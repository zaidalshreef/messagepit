package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/oidcauth"
)

func TestEnvironmentRetentionOnlyPrunesExpiredOwnedRecords(t *testing.T) {
	setup("retention_test")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Retention")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environment: %+v %v", envs, err)
	}
	env := envs[0].ID
	if seconds, err := EnvironmentRetention(ctx, env); err != nil || seconds != 0 {
		t.Fatalf("default retention: %d %v", seconds, err)
	}
	if err := SetEnvironmentRetention(ctx, env, 3599); !errors.Is(err, ErrInvalidRetention) {
		t.Fatalf("invalid age accepted: %v", err)
	}
	if err := SetEnvironmentRetention(ctx, env, 3600); err != nil {
		t.Fatal(err)
	}
	message := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: test\r\n\r\nBody")
	oldEmail, err := StoreInEnvironment(ctx, env, &message, nil)
	if err != nil {
		t.Fatal(err)
	}
	newEmail, err := StoreInEnvironment(ctx, env, &message, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyEmail, err := Store(&message, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldSMS, err := StoreSMSInEnvironment(ctx, env, "a", "b", "expired", "", "")
	if err != nil {
		t.Fatal(err)
	}
	newSMS, err := StoreSMSInEnvironment(ctx, env, "a", "b", "fresh", "", "")
	if err != nil {
		t.Fatal(err)
	}
	oldHook, err := StoreWebhookInEnvironment(ctx, env, "POST", "/", "", nil, []byte("old"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	newHook, err := StoreWebhookInEnvironment(ctx, env, "POST", "/", "", nil, []byte("new"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour).UnixMilli()
	for _, item := range []struct{ table, id string }{{"mailbox", oldEmail}, {"mailbox", legacyEmail}, {"sms_mailbox", oldSMS}, {"webhook_requests", oldHook}} {
		if _, err := db.Exec(fmt.Sprintf(`UPDATE %s SET Created = ? WHERE ID = ?`, tenant(item.table)), oldTime, item.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneEnvironmentRecords(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ kind, old, fresh string }{{"email", oldEmail, newEmail}, {"sms", oldSMS, newSMS}, {"webhook", oldHook, newHook}} {
		if ok, err := RecordInEnvironment(ctx, item.kind, item.old, env); err != nil || ok {
			t.Errorf("expired %s survived: %v %v", item.kind, ok, err)
		}
		if ok, err := RecordInEnvironment(ctx, item.kind, item.fresh, env); err != nil || !ok {
			t.Errorf("fresh %s was pruned: %v %v", item.kind, ok, err)
		}
	}
	if ok, err := RecordInEnvironment(ctx, "email", legacyEmail, LegacyEnvironmentID); err != nil || !ok {
		t.Fatalf("another environment was pruned: %v %v", ok, err)
	}
	var count int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE ID = ?`, tenant("mailbox_data")), oldEmail).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired email body survived: %d %v", count, err)
	}
	if err := SetEnvironmentRetention(ctx, env, 0); err != nil {
		t.Fatal(err)
	}
	if seconds, err := EnvironmentRetention(ctx, env); err != nil || seconds != 0 {
		t.Fatalf("retention was not disabled: %d %v", seconds, err)
	}
}

func TestLegacyMailboxLimitDoesNotPruneProjectMail(t *testing.T) {
	setup("retention_legacy_limit")
	defer Close()
	previousOIDC, previousMax := config.OIDC, config.MaxMessages
	defer func() { config.OIDC, config.MaxMessages = previousOIDC, previousMax }()
	config.OIDC = oidcauth.Config{Issuer: "https://id.example"}
	config.MaxMessages = 1
	ctx := context.Background()
	group, err := CreateGroup(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	message := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: test\r\n\r\nBody")
	for i := 0; i < 2; i++ {
		if _, err := StoreInEnvironment(ctx, envs[0].ID, &message, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := Store(&message, nil); err != nil {
			t.Fatal(err)
		}
	}
	pruneMessages()
	projectMail, err := ListEnvironmentMessages(envs[0].ID, 0, 0, 10)
	if err != nil || len(projectMail) != 2 {
		t.Fatalf("instance limit pruned project messages: %+v %v", projectMail, err)
	}
	legacy, err := ListEnvironmentMessages(LegacyEnvironmentID, 0, 0, 10)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy limit did not apply: %+v %v", legacy, err)
	}
}
