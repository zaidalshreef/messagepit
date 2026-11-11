package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/coreydaley/messagepit/internal/oidcauth"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidLocalUser   = errors.New("invalid username or password")
	ErrLocalUserExists    = errors.New("username already exists")
	ErrLocalUserNotFound  = errors.New("local user not found")
	ErrLocalAdministrator = errors.New("cannot disable the local administrator")
	ErrLocalCredentials   = errors.New("invalid credentials")
	localUsernamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
)

// A fixed bcrypt hash ensures unknown users take the same password-check path.
const dummyLocalPasswordHash = "$2a$12$zGnvsB.mf1gIB2lRpA.od.Uy7DDhZPGyXAcJSSKXeleTjzk8jxju"

// LocalUser contains account metadata, never a password or password hash.
type LocalUser struct {
	Username string    `json:"username"`
	Admin    bool      `json:"admin"`
	Disabled bool      `json:"disabled"`
	Created  time.Time `json:"created_at"`
}

func validLocalCredentials(username, password string) bool {
	return localUsernamePattern.MatchString(username) && len(password) >= 12 && len(password) <= 72
}

// BootstrapLocalAdmin creates the first administrator once, without changing an existing account.
func BootstrapLocalAdmin(ctx context.Context, username, password string) error {
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tenant("local_users"))).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if !validLocalCredentials(username, password) {
		return ErrInvalidLocalUser
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tenant("local_users"))).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		query := fmt.Sprintf(`INSERT INTO %s (Username, PasswordHash, Admin, Created) VALUES (?, ?, 1, ?)`, tenant("local_users"))
		if _, err := tx.ExecContext(ctx, query, username, string(hash), time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CreateLocalUser creates an unprivileged account. Group and project grants remain explicit.
func CreateLocalUser(ctx context.Context, username, password string) error {
	if !validLocalCredentials(username, password) {
		return ErrInvalidLocalUser
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s (Username, PasswordHash, Created) VALUES (?, ?, ?) ON CONFLICT(Username) DO NOTHING`, tenant("local_users"))
	result, err := db.ExecContext(ctx, query, username, string(hash), time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrLocalUserExists
	}
	return nil
}

// AuthenticateLocalUser verifies credentials and returns the corresponding principal.
func AuthenticateLocalUser(ctx context.Context, username, password string) (oidcauth.Principal, error) {
	var hash string
	var admin, disabled int
	var version int64
	query := fmt.Sprintf(`SELECT PasswordHash, Admin, Disabled, Version FROM %s WHERE Username = ?`, tenant("local_users"))
	err := db.QueryRowContext(ctx, query, username).Scan(&hash, &admin, &disabled, &version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return oidcauth.Principal{}, err
	}
	if err != nil {
		hash = dummyLocalPasswordHash
	}
	passwordBytes := []byte(password)
	validLength := len(passwordBytes) <= 72
	if !validLength {
		passwordBytes = passwordBytes[:72]
	}
	match := bcrypt.CompareHashAndPassword([]byte(hash), passwordBytes) == nil
	if err != nil || disabled != 0 || !validLength || !match {
		return oidcauth.Principal{}, ErrLocalCredentials
	}
	return oidcauth.Principal{Issuer: oidcauth.LocalIssuer, Subject: username, Admin: admin != 0, LocalVersion: version}, nil
}

// LocalUserActive rechecks a local identity when its session is used.
func LocalUserActive(ctx context.Context, principal oidcauth.Principal) bool {
	if principal.Issuer != oidcauth.LocalIssuer {
		return false
	}
	var admin, disabled int
	var version int64
	query := fmt.Sprintf(`SELECT Admin, Disabled, Version FROM %s WHERE Username = ?`, tenant("local_users"))
	return db.QueryRowContext(ctx, query, principal.Subject).Scan(&admin, &disabled, &version) == nil && disabled == 0 && (admin != 0) == principal.Admin && version == principal.LocalVersion
}

// ChangeLocalPassword invalidates all previously issued sessions for the account.
func ChangeLocalPassword(ctx context.Context, principal oidcauth.Principal, current, replacement string) error {
	if principal.Issuer != oidcauth.LocalIssuer || !validLocalCredentials(principal.Subject, replacement) {
		return ErrInvalidLocalUser
	}
	verified, err := AuthenticateLocalUser(ctx, principal.Subject, current)
	if err != nil || verified.LocalVersion != principal.LocalVersion {
		return ErrLocalCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(replacement), 12)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`UPDATE %s SET PasswordHash = ?, Version = Version + 1 WHERE Username = ? AND Version = ? AND Disabled = 0`, tenant("local_users"))
	result, err := db.ExecContext(ctx, query, string(hash), principal.Subject, principal.LocalVersion)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrLocalCredentials
	}
	return nil
}

// ResetLocalPassword replaces a non-administrator's password and invalidates their sessions.
func ResetLocalPassword(ctx context.Context, username, password string) error {
	return resetLocalPassword(ctx, username, password, false)
}

// ResetLocalAdministrator is intended for offline recovery by a machine operator.
func ResetLocalAdministrator(ctx context.Context, username, password string) error {
	return resetLocalPassword(ctx, username, password, true)
}

func resetLocalPassword(ctx context.Context, username, password string, admin bool) error {
	if !validLocalCredentials(username, password) {
		return ErrInvalidLocalUser
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`UPDATE %s SET PasswordHash = ?, Version = Version + 1 WHERE Username = ? AND Admin = ? AND Disabled = 0`, tenant("local_users"))
	adminValue := 0
	if admin {
		adminValue = 1
	}
	result, err := db.ExecContext(ctx, query, string(hash), username, adminValue)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrLocalUserNotFound
	}
	return nil
}

// ListLocalUsers returns account metadata for administrators.
func ListLocalUsers(ctx context.Context) ([]LocalUser, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT Username, Admin, Disabled, Created FROM %s ORDER BY Username`, tenant("local_users")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []LocalUser{}
	for rows.Next() {
		var user LocalUser
		var admin, disabled int
		var created int64
		if err := rows.Scan(&user.Username, &admin, &disabled, &created); err != nil {
			return nil, err
		}
		user.Admin, user.Disabled, user.Created = admin != 0, disabled != 0, time.UnixMilli(created).UTC()
		users = append(users, user)
	}
	return users, rows.Err()
}

// DisableLocalUser permanently disables a non-administrator and removes their grants.
func DisableLocalUser(ctx context.Context, username string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin int
	query := fmt.Sprintf(`SELECT Admin FROM %s WHERE Username = ?`, tenant("local_users"))
	if err := tx.QueryRowContext(ctx, query, username).Scan(&admin); errors.Is(err, sql.ErrNoRows) {
		return ErrLocalUserNotFound
	} else if err != nil {
		return err
	}
	if admin != 0 {
		return ErrLocalAdministrator
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET Disabled = 1 WHERE Username = ?`, tenant("local_users")), username); err != nil {
		return err
	}
	for _, table := range []string{"project_memberships", "group_memberships"} {
		query := fmt.Sprintf(`DELETE FROM %s WHERE Issuer = ? AND Subject = ?`, tenant(table))
		if _, err := tx.ExecContext(ctx, query, oidcauth.LocalIssuer, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}
