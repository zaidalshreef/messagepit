package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/internal/scopedevents"
)

func TestScopedEventsFollowCommittedChanges(t *testing.T) {
	setup("scoped_events")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Events")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "Events")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	env := envs[0].ID
	updates, stop, err := scopedevents.Subscribe(env)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := StoreSMSInEnvironment(ctx, env, "a", "b", "message", "", strings.Repeat("a", 129)); err == nil {
		t.Fatal("invalid correlation ID accepted")
	}
	select {
	case <-updates:
		t.Fatal("failed transaction sent notification")
	default:
	}
	id, err := StoreSMSInEnvironment(ctx, env, "a", "b", "message", "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	default:
		t.Fatal("committed capture was not announced")
	}
	if err := DeleteEnvironmentRecord(ctx, "sms", id, env); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	default:
		t.Fatal("committed deletion was not announced")
	}
	if err := DeleteEnvironmentRecord(ctx, "sms", id, env); err == nil {
		t.Fatal("missing record deleted twice")
	}
	select {
	case <-updates:
		t.Fatal("failed deletion sent notification")
	default:
	}
	if err := SetEnvironmentRetention(ctx, env, 3600); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreSMSInEnvironment(ctx, env, "a", "b", "expired", "", ""); err != nil {
		t.Fatal(err)
	}
	<-updates
	if err := PruneEnvironmentRecords(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	default:
		t.Fatal("retention deletion was not announced")
	}
	if err := PruneEnvironmentRecords(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
		t.Fatal("empty retention cleanup sent notification")
	default:
	}
}
