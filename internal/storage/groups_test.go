package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/coreydaley/messagepit/config"
)

func TestGroupsAndMemberships(t *testing.T) {
	setup("groups_test")
	defer Close()
	ctx := context.Background()
	first, err := CreateGroup(ctx, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateGroup(ctx, "QA")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateGroup(ctx, "Engineering"); !errors.Is(err, ErrGroupExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := CreateGroup(ctx, "  "); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("blank name: %v", err)
	}
	for _, member := range []struct{ group, issuer, subject string }{
		{first.ID, "https://id.example", "alice"},
		{first.ID, "https://id.example", "bob"},
		{second.ID, "https://id.example", "bob"},
	} {
		if err := AddGroupMember(ctx, member.group, member.issuer, member.subject); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddGroupMember(ctx, first.ID, "https://id.example", "alice"); err != nil {
		t.Fatalf("duplicate membership should be idempotent: %v", err)
	}
	if err := AddGroupMember(ctx, "missing", "https://id.example", "alice"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("membership of missing group: %v", err)
	}

	adminGroups, err := ListGroups(ctx, "https://id.example", "admin", true)
	if err != nil || len(adminGroups) != 3 {
		t.Fatalf("administrator groups: %v, %v", adminGroups, err)
	}
	aliceGroups, err := ListGroups(ctx, "https://id.example", "alice", false)
	if err != nil || len(aliceGroups) != 1 || aliceGroups[0].ID != first.ID {
		t.Fatalf("Alice's groups: %v, %v", aliceGroups, err)
	}
	otherIssuer, err := ListGroups(ctx, "https://other.example", "alice", false)
	if err != nil || len(otherIssuer) != 0 {
		t.Fatalf("same subject at another issuer matched: %v, %v", otherIssuer, err)
	}
	members, err := ListGroupMembers(ctx, first.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("group members: %v, %v", members, err)
	}
	if err := RemoveGroupMember(ctx, first.ID, "https://id.example", "alice"); err != nil {
		t.Fatal(err)
	}
	if found, err := GroupMemberExists(ctx, first.ID, "https://id.example", "alice"); err != nil || found {
		t.Fatalf("removed member still exists: %v, %v", found, err)
	}
	if err := DeleteGroup(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if found, err := GroupMemberExists(ctx, first.ID, "https://id.example", "bob"); err != nil || found {
		t.Fatalf("deleted group retained memberships: %v, %v", found, err)
	}
	if err := DeleteGroup(ctx, first.ID); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("delete missing group: %v", err)
	}
}

func TestGroupMigrationPreservesExistingMail(t *testing.T) {
	previousDB, previousTenant := config.Database, config.TenantID
	t.Cleanup(func() { config.Database, config.TenantID = previousDB, previousTenant })
	config.Database = filepath.Join(t.TempDir(), "existing.db")
	config.TenantID = ""
	if err := InitDB(); err != nil {
		t.Fatal(err)
	}
	message, err := os.ReadFile("testdata/plain-text.eml")
	if err != nil {
		t.Fatal(err)
	}
	id, err := Store(&message, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TRIGGER delete_email_correlation`, `DROP TRIGGER delete_sms_correlation`, `DROP TRIGGER delete_webhook_correlation`,
		`DROP TRIGGER delete_email_ownership`, `DROP TRIGGER delete_sms_ownership`, `DROP TRIGGER delete_webhook_ownership`,
		`DROP TABLE capture_correlations`, `DROP TABLE audit_events`, `DROP TABLE environment_retention`, `DROP TABLE service_tokens`, `DROP TABLE message_ownership`, `DROP TABLE project_memberships`, `DROP TABLE project_environments`,
		`DROP TABLE team_projects`, `DROP TABLE group_memberships`, `DROP TABLE team_groups`,
		`DELETE FROM schemas WHERE Version IN ('4.0.0', '4.1.0', '4.2.0', '4.3.0', '4.4.0', '4.5.0', '4.6.0', '4.7.0')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	Close()
	if err := InitDB(); err != nil {
		t.Fatal(err)
	}
	defer Close()
	if _, err := GetMessage(id); err != nil {
		t.Fatalf("existing email lost after migration: %v", err)
	}
	if _, err := CreateGroup(context.Background(), "Migrated"); err != nil {
		t.Fatalf("group tables not created after migration: %v", err)
	}
	project, err := GetProject(context.Background(), LegacyProjectID)
	if err != nil || project.GroupID != LegacyGroupID {
		t.Fatalf("legacy default project missing: %+v, %v", project, err)
	}
	role, err := ProjectRole(context.Background(), LegacyProjectID, "https://id.example", "user")
	if err != nil || role.Valid() {
		t.Fatalf("legacy project granted to a non-administrator: %q, %v", role, err)
	}
}

func TestLegacyMigrationWithExistingGroupName(t *testing.T) {
	previousDB, previousTenant := config.Database, config.TenantID
	t.Cleanup(func() { config.Database, config.TenantID = previousDB, previousTenant })
	config.Database = filepath.Join(t.TempDir(), "existing-groups.db")
	config.TenantID = ""
	if err := InitDB(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DROP TRIGGER delete_email_correlation`, `DROP TRIGGER delete_sms_correlation`, `DROP TRIGGER delete_webhook_correlation`,
		`DROP TRIGGER delete_email_ownership`, `DROP TRIGGER delete_sms_ownership`, `DROP TRIGGER delete_webhook_ownership`,
		`DROP TABLE capture_correlations`, `DROP TABLE audit_events`, `DROP TABLE environment_retention`, `DROP TABLE service_tokens`, `DROP TABLE message_ownership`,
		`DELETE FROM project_environments WHERE ID = '__messagepit_legacy_environment__'`,
		`DELETE FROM team_projects WHERE ID = '__messagepit_legacy_project__'`,
		`DELETE FROM team_groups WHERE ID = '__messagepit_legacy_group__'`,
		`INSERT INTO team_groups (ID, Name, Created) VALUES ('existing', 'Legacy data', 1)`,
		`DELETE FROM schemas WHERE Version IN ('4.2.0', '4.3.0', '4.4.0', '4.5.0', '4.6.0', '4.7.0')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	Close()
	if err := InitDB(); err != nil {
		t.Fatalf("migration failed with an existing group name: %v", err)
	}
	defer Close()
	groups, err := ListGroups(context.Background(), "", "", true)
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups after migration: %+v %v", groups, err)
	}
	project, err := GetProject(context.Background(), LegacyProjectID)
	if err != nil || project.GroupID != LegacyGroupID {
		t.Fatalf("legacy project after migration: %+v %v", project, err)
	}
}
