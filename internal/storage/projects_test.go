package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/coreydaley/messagepit/internal/access"
)

func TestProjectGrantsAreExplicitAndRevokedWithGroupMembership(t *testing.T) {
	setup("project_test")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"alice", "bob"} {
		if err := AddGroupMember(ctx, group.ID, "https://id.example", subject); err != nil {
			t.Fatal(err)
		}
	}
	first, err := CreateProject(ctx, group.ID, "Project A")
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateProject(ctx, group.ID, "Project B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(ctx, group.ID, "Project A"); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("duplicate project name: %v", err)
	}
	if _, err := CreateProject(ctx, "missing", "Project"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("project in missing group: %v", err)
	}
	if err := GrantProjectRole(ctx, first.ID, "https://id.example", "outsider", access.Viewer); !errors.Is(err, ErrGroupMembershipRequired) {
		t.Fatalf("non-member grant: %v", err)
	}
	if err := GrantProjectRole(ctx, first.ID, "https://id.example", "alice", access.Role("unknown")); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("unknown role grant: %v", err)
	}
	if err := GrantProjectRole(ctx, first.ID, "https://id.example", "alice", access.Viewer); err != nil {
		t.Fatal(err)
	}
	if err := GrantProjectRole(ctx, second.ID, "https://id.example", "bob", access.Developer); err != nil {
		t.Fatal(err)
	}
	aliceProjects, err := ListProjects(ctx, group.ID, "https://id.example", "alice", false)
	if err != nil || len(aliceProjects) != 1 || aliceProjects[0].ID != first.ID {
		t.Fatalf("Alice's projects: %+v, %v", aliceProjects, err)
	}
	role, err := ProjectRole(ctx, second.ID, "https://id.example", "alice")
	if err != nil || role.Valid() {
		t.Fatalf("group membership granted another project: %q, %v", role, err)
	}
	adminProjects, err := ListProjects(ctx, group.ID, "https://id.example", "admin", true)
	if err != nil || len(adminProjects) != 2 {
		t.Fatalf("administrator projects: %+v, %v", adminProjects, err)
	}
	environments, err := ListProjectEnvironments(ctx, first.ID)
	if err != nil || len(environments) != 1 || environments[0].Name != "default" {
		t.Fatalf("default environment: %+v, %v", environments, err)
	}
	if _, err := CreateProjectEnvironment(ctx, first.ID, "staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProjectEnvironment(ctx, first.ID, "staging"); !errors.Is(err, ErrEnvironmentExists) {
		t.Fatalf("duplicate environment name: %v", err)
	}
	if err := RevokeProjectRole(ctx, second.ID, "https://id.example", "bob"); err != nil {
		t.Fatal(err)
	}
	if role, err := ProjectRole(ctx, second.ID, "https://id.example", "bob"); err != nil || role.Valid() {
		t.Fatalf("revoked project role retained access: %q, %v", role, err)
	}
	if err := GrantProjectRole(ctx, second.ID, "https://id.example", "bob", access.Developer); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup(ctx, group.ID); !errors.Is(err, ErrGroupHasProjects) {
		t.Fatalf("group with projects was deleted: %v", err)
	}
	if err := RemoveGroupMember(ctx, group.ID, "https://id.example", "alice"); err != nil {
		t.Fatal(err)
	}
	role, err = ProjectRole(ctx, first.ID, "https://id.example", "alice")
	if err != nil || role.Valid() {
		t.Fatalf("group removal did not revoke project grant: %q, %v", role, err)
	}
	bobRole, err := ProjectRole(ctx, second.ID, "https://id.example", "bob")
	if err != nil || bobRole != access.Developer {
		t.Fatalf("unrelated grant changed: %q, %v", bobRole, err)
	}
}
