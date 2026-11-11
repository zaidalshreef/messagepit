package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
)

func TestLocalAccountsAndGrantRevocation(t *testing.T) {
	previousDB, previousTenant := config.Database, config.TenantID
	t.Cleanup(func() { config.Database, config.TenantID = previousDB, previousTenant })
	config.Database, config.TenantID = filepath.Join(t.TempDir(), "local.db"), ""
	if err := InitDB(); err != nil {
		t.Fatal(err)
	}
	defer Close()
	ctx := context.Background()
	if err := BootstrapLocalAdmin(ctx, "admin", "short"); !errors.Is(err, ErrInvalidLocalUser) {
		t.Fatalf("weak bootstrap password accepted: %v", err)
	}
	if err := BootstrapLocalAdmin(ctx, "admin", "a-long-admin-password"); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapLocalAdmin(ctx, "admin", "a-different-password"); err != nil {
		t.Fatal(err)
	}
	admin, err := AuthenticateLocalUser(ctx, "admin", "a-long-admin-password")
	if err != nil || !admin.Admin || !LocalUserActive(ctx, admin) {
		t.Fatal("bootstrap administrator could not authenticate")
	}
	if _, err := AuthenticateLocalUser(ctx, "admin", "a-different-password"); !errors.Is(err, ErrLocalCredentials) {
		t.Fatal("bootstrap changed an existing password")
	}
	for _, username := range []string{"admin", "missing"} {
		if _, err := AuthenticateLocalUser(ctx, username, "wrong-password"); !errors.Is(err, ErrLocalCredentials) {
			t.Fatal("invalid credentials were not rejected uniformly")
		}
	}
	var storedHash string
	if err := db.QueryRow(`SELECT PasswordHash FROM local_users WHERE Username = 'admin'`).Scan(&storedHash); err != nil || storedHash == "a-long-admin-password" || len(storedHash) == 0 {
		t.Fatal("administrator password was not stored as a hash")
	}
	if err := CreateLocalUser(ctx, "Alice", "a-long-user-password"); !errors.Is(err, ErrInvalidLocalUser) {
		t.Fatal("invalid username accepted")
	}
	if err := CreateLocalUser(ctx, "alice", "a-long-user-password"); err != nil {
		t.Fatal(err)
	}
	user, err := AuthenticateLocalUser(ctx, "alice", "a-long-user-password")
	if err != nil || user.Admin {
		t.Fatal("new local user could not authenticate as an unprivileged user")
	}
	group, err := CreateGroup(ctx, "Local users")
	if err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, group.ID, "Local project")
	if err != nil {
		t.Fatal(err)
	}
	if err := AddGroupMember(ctx, group.ID, oidcauth.LocalIssuer, "missing"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatal("a nonexistent local account was added to a group")
	}
	if err := AddGroupMember(ctx, group.ID, oidcauth.LocalIssuer, "alice"); err != nil {
		t.Fatal(err)
	}
	if role, err := ProjectRole(ctx, project.ID, user.Issuer, user.Subject); err != nil || role.Valid() {
		t.Fatal("group membership granted project access")
	}
	if err := GrantProjectRole(ctx, project.ID, user.Issuer, user.Subject, access.Viewer); err != nil {
		t.Fatal(err)
	}
	if err := ChangeLocalPassword(ctx, user, "wrong-password", "another-long-password"); !errors.Is(err, ErrLocalCredentials) {
		t.Fatal("password changed without the current password")
	}
	if err := ChangeLocalPassword(ctx, user, "a-long-user-password", "another-long-password"); err != nil {
		t.Fatal(err)
	}
	if LocalUserActive(ctx, user) {
		t.Fatal("password change did not invalidate the previous session")
	}
	user, err = AuthenticateLocalUser(ctx, "alice", "another-long-password")
	if err != nil || !LocalUserActive(ctx, user) {
		t.Fatal("new password did not authenticate")
	}
	if err := ResetLocalPassword(ctx, "alice", "administrator-reset-password"); err != nil {
		t.Fatal(err)
	}
	if LocalUserActive(ctx, user) {
		t.Fatal("administrator password reset did not invalidate the previous session")
	}
	user, err = AuthenticateLocalUser(ctx, "alice", "administrator-reset-password")
	if err != nil || !LocalUserActive(ctx, user) {
		t.Fatal("reset password did not authenticate")
	}
	if err := ResetLocalPassword(ctx, "admin", "administrator-reset-password"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatal("ordinary user reset could modify an administrator")
	}
	if err := ResetLocalAdministrator(ctx, "admin", "admin-recovery-password"); err != nil {
		t.Fatal(err)
	}
	if LocalUserActive(ctx, admin) {
		t.Fatal("administrator recovery did not invalidate the old session")
	}
	if _, err := AuthenticateLocalUser(ctx, "admin", "admin-recovery-password"); err != nil {
		t.Fatal("recovered administrator password did not authenticate")
	}
	if err := DisableLocalUser(ctx, "admin"); !errors.Is(err, ErrLocalAdministrator) {
		t.Fatal("bootstrap administrator could be disabled")
	}
	if err := DisableLocalUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if LocalUserActive(ctx, user) {
		t.Fatal("disabled account retained an active session")
	}
	if _, err := AuthenticateLocalUser(ctx, "alice", "administrator-reset-password"); !errors.Is(err, ErrLocalCredentials) {
		t.Fatal("disabled account could authenticate")
	}
	if member, err := GroupMemberExists(ctx, group.ID, user.Issuer, user.Subject); err != nil || member {
		t.Fatal("disabled account retained group membership")
	}
	if role, err := ProjectRole(ctx, project.ID, user.Issuer, user.Subject); err != nil || role.Valid() {
		t.Fatal("disabled account retained a project grant")
	}
	if err := CreateLocalUser(ctx, "alice", "a-new-user-password"); !errors.Is(err, ErrLocalUserExists) {
		t.Fatal("disabled username was reused")
	}
	if err := AddGroupMember(ctx, group.ID, oidcauth.LocalIssuer, "alice"); !errors.Is(err, ErrLocalUserNotFound) {
		t.Fatal("disabled account was added back to a group")
	}
}

func TestNewSharedDatabaseIsPrivate(t *testing.T) {
	previousDB, previousTenant, previousLocal := config.Database, config.TenantID, config.LocalAuth
	t.Cleanup(func() { config.Database, config.TenantID, config.LocalAuth = previousDB, previousTenant, previousLocal })
	config.Database, config.TenantID, config.LocalAuth = filepath.Join(t.TempDir(), "private.db"), "", true
	if err := InitDB(); err != nil {
		t.Fatal(err)
	}
	defer Close()
	info, err := os.Stat(config.Database)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("new shared database allows group or other access: %04o", info.Mode().Perm())
	}
}
