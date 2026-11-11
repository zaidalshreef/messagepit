package storage

import (
	"context"
	"errors"
	"testing"
)

func TestCorrelationsAreScopedAndRemovedWithRecords(t *testing.T) {
	setup("correlation_test")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Correlation")
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
	env := envs[0].ID
	email := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: test\r\nX-MessagePit-Correlation-ID: flow-123\r\n\r\nBody")
	emailID, err := StoreInEnvironment(ctx, env, &email, nil)
	if err != nil {
		t.Fatal(err)
	}
	smsID, err := StoreSMSInEnvironment(ctx, env, "a", "b", "text", "", "flow-123")
	if err != nil {
		t.Fatal(err)
	}
	hookID, err := StoreWebhookInEnvironment(ctx, env, "POST", "/", "", map[string][]string{CorrelationHeader: {"flow-123"}}, []byte("body"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreSMSInEnvironment(ctx, LegacyEnvironmentID, "a", "b", "other", "", "flow-123"); err != nil {
		t.Fatal(err)
	}
	records, err := ListCorrelatedRecords(ctx, env, "flow-123")
	if err != nil || len(records) != 3 {
		t.Fatalf("environment correlations: %+v %v", records, err)
	}
	legacy, err := ListCorrelatedRecords(ctx, LegacyEnvironmentID, "flow-123")
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy correlations: %+v %v", legacy, err)
	}
	if _, err := StoreSMSInEnvironment(ctx, env, "a", "b", "invalid", "", "bad id"); !errors.Is(err, ErrInvalidCorrelationID) {
		t.Fatalf("invalid ID accepted: %v", err)
	}
	for _, record := range []struct{ kind, id string }{{"email", emailID}, {"sms", smsID}, {"webhook", hookID}} {
		if err := DeleteEnvironmentRecord(ctx, record.kind, record.id, env); err != nil {
			t.Fatal(err)
		}
	}
	records, err = ListCorrelatedRecords(ctx, env, "flow-123")
	if err != nil || len(records) != 0 {
		t.Fatalf("deleted records retained correlations: %+v %v", records, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tenant("capture_correlations")).Scan(&count); err != nil || count != 1 {
		t.Fatalf("correlation cleanup: %d %v", count, err)
	}
}

func TestCorrelationIDValidation(t *testing.T) {
	for _, value := range []string{"", "pipeline-42.job:7", "a_b.c"} {
		if !ValidCorrelationID(value) {
			t.Errorf("valid correlation ID rejected: %q", value)
		}
	}
	for _, value := range []string{".", "..", "bad id", "bad/path", "bad\nheader"} {
		if ValidCorrelationID(value) {
			t.Errorf("invalid correlation ID accepted: %q", value)
		}
	}
}
