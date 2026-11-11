package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/lithammer/shortuuid/v4"
)

var (
	ErrGroupNotFound    = errors.New("group not found")
	ErrGroupExists      = errors.New("group already exists")
	ErrInvalidGroup     = errors.New("invalid group or member")
	ErrGroupHasProjects = errors.New("group contains projects")
)

// Group is a named collection of explicitly assigned members.
type Group struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created_at"`
}

// GroupMember identifies an OIDC subject at a particular issuer.
type GroupMember struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

func validGroupText(value string, max int) bool {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// CreateGroup creates an empty group. Membership does not grant project access.
func CreateGroup(ctx context.Context, name string) (Group, error) {
	name = strings.TrimSpace(name)
	if !validGroupText(name, 100) {
		return Group{}, ErrInvalidGroup
	}
	group := Group{ID: shortuuid.New(), Name: name, Created: time.Now().UTC().Truncate(time.Millisecond)}
	query := fmt.Sprintf(`INSERT INTO %s (ID, Name, Created) VALUES (?, ?, ?) ON CONFLICT(Name) DO NOTHING`, tenant("team_groups"))
	result, err := db.ExecContext(ctx, query, group.ID, group.Name, group.Created.UnixMilli())
	if err != nil {
		return Group{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Group{}, err
	}
	if count == 0 {
		return Group{}, ErrGroupExists
	}
	return group, nil
}

// ListGroups returns every group for administrators and only memberships for other users.
func ListGroups(ctx context.Context, issuer, subject string, admin bool) ([]Group, error) {
	query := fmt.Sprintf(`SELECT g.ID, g.Name, g.Created FROM %s AS g`, tenant("team_groups"))
	args := []any{}
	if !admin {
		query += fmt.Sprintf(` JOIN %s AS m ON m.GroupID = g.ID WHERE m.Issuer = ? AND m.Subject = ?`, tenant("group_memberships"))
		args = append(args, issuer, subject)
	}
	query += ` ORDER BY g.Name, g.ID`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var group Group
		var created int64
		if err := rows.Scan(&group.ID, &group.Name, &created); err != nil {
			return nil, err
		}
		group.Created = time.UnixMilli(created).UTC()
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// GetGroup returns group metadata by ID.
func GetGroup(ctx context.Context, id string) (Group, error) {
	var group Group
	var created int64
	query := fmt.Sprintf(`SELECT ID, Name, Created FROM %s WHERE ID = ?`, tenant("team_groups"))
	err := db.QueryRowContext(ctx, query, id).Scan(&group.ID, &group.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupNotFound
	}
	if err != nil {
		return Group{}, err
	}
	group.Created = time.UnixMilli(created).UTC()
	return group, nil
}

// GroupMemberExists checks membership without exposing another group's members.
func GroupMemberExists(ctx context.Context, groupID, issuer, subject string) (bool, error) {
	var exists int
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE GroupID = ? AND Issuer = ? AND Subject = ?)`, tenant("group_memberships"))
	err := db.QueryRowContext(ctx, query, groupID, issuer, subject).Scan(&exists)
	return exists == 1, err
}

// ListGroupMembers returns the members of an existing group.
func ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	if err := requireGroup(ctx, db, groupID); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`SELECT Issuer, Subject FROM %s WHERE GroupID = ? ORDER BY Issuer, Subject`, tenant("group_memberships"))
	rows, err := db.QueryContext(ctx, query, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []GroupMember{}
	for rows.Next() {
		var member GroupMember
		if err := rows.Scan(&member.Issuer, &member.Subject); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// AddGroupMember assigns a subject to a group. Repeated additions are idempotent.
func AddGroupMember(ctx context.Context, groupID, issuer, subject string) error {
	if !validGroupText(issuer, 2048) || !validGroupText(subject, 255) {
		return ErrInvalidGroup
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireGroup(ctx, tx, groupID); err != nil {
		return err
	}
	if issuer == oidcauth.LocalIssuer {
		var active int
		query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE Username = ? AND Disabled = 0)`, tenant("local_users"))
		if err := tx.QueryRowContext(ctx, query, subject).Scan(&active); err != nil {
			return err
		}
		if active == 0 {
			return ErrLocalUserNotFound
		}
	}
	query := fmt.Sprintf(`INSERT INTO %s (GroupID, Issuer, Subject) VALUES (?, ?, ?) ON CONFLICT(GroupID, Issuer, Subject) DO NOTHING`, tenant("group_memberships"))
	if _, err := tx.ExecContext(ctx, query, groupID, issuer, subject); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveGroupMember removes a membership from an existing group.
func RemoveGroupMember(ctx context.Context, groupID, issuer, subject string) error {
	if !validGroupText(issuer, 2048) || !validGroupText(subject, 255) {
		return ErrInvalidGroup
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireGroup(ctx, tx, groupID); err != nil {
		return err
	}
	revoke := fmt.Sprintf(`DELETE FROM %s WHERE Issuer = ? AND Subject = ? AND ProjectID IN (SELECT ID FROM %s WHERE GroupID = ?)`, tenant("project_memberships"), tenant("team_projects"))
	if _, err := tx.ExecContext(ctx, revoke, issuer, subject, groupID); err != nil {
		return err
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE GroupID = ? AND Issuer = ? AND Subject = ?`, tenant("group_memberships"))
	if _, err := tx.ExecContext(ctx, query, groupID, issuer, subject); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteGroup removes a group and its memberships atomically.
func DeleteGroup(ctx context.Context, groupID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireGroup(ctx, tx, groupID); err != nil {
		return err
	}
	var hasProjects int
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE GroupID = ?)`, tenant("team_projects"))
	if err := tx.QueryRowContext(ctx, query, groupID).Scan(&hasProjects); err != nil {
		return err
	}
	if hasProjects != 0 {
		return ErrGroupHasProjects
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE GroupID = ?`, tenant("group_memberships")), groupID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE ID = ?`, tenant("team_groups")), groupID); err != nil {
		return err
	}
	return tx.Commit()
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireGroup(ctx context.Context, queryer rowQuerier, groupID string) error {
	var exists int
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE ID = ?)`, tenant("team_groups"))
	if err := queryer.QueryRowContext(ctx, query, groupID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrGroupNotFound
	}
	return nil
}
