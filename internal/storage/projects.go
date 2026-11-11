package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/lithammer/shortuuid/v4"
)

var (
	ErrProjectNotFound         = errors.New("project not found")
	ErrProjectExists           = errors.New("project already exists")
	ErrGroupMembershipRequired = errors.New("group membership is required")
	ErrInvalidProject          = errors.New("invalid project or role")
	ErrEnvironmentExists       = errors.New("environment already exists")
	ErrEnvironmentNotFound     = errors.New("environment not found")
)

// Reserved IDs keep existing captured data in an administrator-only default project.
const (
	LegacyGroupID       = "__messagepit_legacy_group__"
	LegacyProjectID     = "__messagepit_legacy_project__"
	LegacyEnvironmentID = "__messagepit_legacy_environment__"
)

// Project belongs to one group. Access is granted independently of group membership.
type Project struct {
	ID      string    `json:"id"`
	GroupID string    `json:"group_id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created_at"`
}

// ProjectEnvironment identifies a separate development or test environment.
type ProjectEnvironment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Created   time.Time `json:"created_at"`
}

// ProjectMember represents an explicit role grant to an OIDC identity.
type ProjectMember struct {
	Issuer  string      `json:"issuer"`
	Subject string      `json:"subject"`
	Role    access.Role `json:"role"`
}

// GetProjectEnvironment returns an environment by ID.
func GetProjectEnvironment(ctx context.Context, id string) (ProjectEnvironment, error) {
	var environment ProjectEnvironment
	var created int64
	query := fmt.Sprintf(`SELECT ID, ProjectID, Name, Created FROM %s WHERE ID = ?`, tenant("project_environments"))
	err := db.QueryRowContext(ctx, query, id).Scan(&environment.ID, &environment.ProjectID, &environment.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectEnvironment{}, ErrEnvironmentNotFound
	}
	if err != nil {
		return ProjectEnvironment{}, err
	}
	environment.Created = time.UnixMilli(created).UTC()
	return environment, nil
}

// CreateProject creates a project and its default environment atomically.
func CreateProject(ctx context.Context, groupID, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if !validGroupText(name, 100) {
		return Project{}, ErrInvalidProject
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if err := requireGroup(ctx, tx, groupID); err != nil {
		return Project{}, err
	}
	project := Project{ID: shortuuid.New(), GroupID: groupID, Name: name, Created: time.Now().UTC().Truncate(time.Millisecond)}
	query := fmt.Sprintf(`INSERT INTO %s (ID, GroupID, Name, Created) VALUES (?, ?, ?, ?) ON CONFLICT(GroupID, Name) DO NOTHING`, tenant("team_projects"))
	result, err := tx.ExecContext(ctx, query, project.ID, project.GroupID, project.Name, project.Created.UnixMilli())
	if err != nil {
		return Project{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if count == 0 {
		return Project{}, ErrProjectExists
	}
	query = fmt.Sprintf(`INSERT INTO %s (ID, ProjectID, Name, Created) VALUES (?, ?, ?, ?)`, tenant("project_environments"))
	if _, err := tx.ExecContext(ctx, query, shortuuid.New(), project.ID, "default", project.Created.UnixMilli()); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return project, nil
}

// ListProjects returns only explicitly granted projects unless the caller is a system administrator.
func ListProjects(ctx context.Context, groupID, issuer, subject string, admin bool) ([]Project, error) {
	query := fmt.Sprintf(`SELECT p.ID, p.GroupID, p.Name, p.Created FROM %s AS p`, tenant("team_projects"))
	args := []any{}
	if !admin {
		query += fmt.Sprintf(` JOIN %s AS m ON m.ProjectID = p.ID AND m.Issuer = ? AND m.Subject = ?`, tenant("project_memberships"))
		args = append(args, issuer, subject)
	}
	query += ` WHERE p.GroupID = ? ORDER BY p.Name, p.ID`
	args = append(args, groupID)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var project Project
		var created int64
		if err := rows.Scan(&project.ID, &project.GroupID, &project.Name, &created); err != nil {
			return nil, err
		}
		project.Created = time.UnixMilli(created).UTC()
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// GetProject returns project metadata by ID.
func GetProject(ctx context.Context, id string) (Project, error) {
	var project Project
	var created int64
	query := fmt.Sprintf(`SELECT ID, GroupID, Name, Created FROM %s WHERE ID = ?`, tenant("team_projects"))
	err := db.QueryRowContext(ctx, query, id).Scan(&project.ID, &project.GroupID, &project.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	project.Created = time.UnixMilli(created).UTC()
	return project, nil
}

// ListProjectEnvironments returns environments belonging to one project.
func ListProjectEnvironments(ctx context.Context, projectID string) ([]ProjectEnvironment, error) {
	if _, err := GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`SELECT ID, ProjectID, Name, Created FROM %s WHERE ProjectID = ? ORDER BY Name, ID`, tenant("project_environments"))
	rows, err := db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	environments := []ProjectEnvironment{}
	for rows.Next() {
		var environment ProjectEnvironment
		var created int64
		if err := rows.Scan(&environment.ID, &environment.ProjectID, &environment.Name, &created); err != nil {
			return nil, err
		}
		environment.Created = time.UnixMilli(created).UTC()
		environments = append(environments, environment)
	}
	return environments, rows.Err()
}

// CreateProjectEnvironment creates a named environment inside a project.
func CreateProjectEnvironment(ctx context.Context, projectID, name string) (ProjectEnvironment, error) {
	name = strings.TrimSpace(name)
	if !validGroupText(name, 100) {
		return ProjectEnvironment{}, ErrInvalidProject
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	defer tx.Rollback()
	if _, err := projectGroup(ctx, tx, projectID); err != nil {
		return ProjectEnvironment{}, err
	}
	environment := ProjectEnvironment{ID: shortuuid.New(), ProjectID: projectID, Name: name, Created: time.Now().UTC().Truncate(time.Millisecond)}
	query := fmt.Sprintf(`INSERT INTO %s (ID, ProjectID, Name, Created) VALUES (?, ?, ?, ?) ON CONFLICT(ProjectID, Name) DO NOTHING`, tenant("project_environments"))
	result, err := tx.ExecContext(ctx, query, environment.ID, environment.ProjectID, environment.Name, environment.Created.UnixMilli())
	if err != nil {
		return ProjectEnvironment{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ProjectEnvironment{}, err
	}
	if count == 0 {
		return ProjectEnvironment{}, ErrEnvironmentExists
	}
	if err := tx.Commit(); err != nil {
		return ProjectEnvironment{}, err
	}
	return environment, nil
}

// GrantProjectRole assigns a role to an existing member of the project's group.
func GrantProjectRole(ctx context.Context, projectID, issuer, subject string, role access.Role) error {
	if !validGroupText(issuer, 2048) || !validGroupText(subject, 255) || !role.Valid() {
		return ErrInvalidProject
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	groupID, err := projectGroup(ctx, tx, projectID)
	if err != nil {
		return err
	}
	var member int
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE GroupID = ? AND Issuer = ? AND Subject = ?)`, tenant("group_memberships"))
	if err := tx.QueryRowContext(ctx, query, groupID, issuer, subject).Scan(&member); err != nil {
		return err
	}
	if member == 0 {
		return ErrGroupMembershipRequired
	}
	query = fmt.Sprintf(`INSERT INTO %s (ProjectID, Issuer, Subject, Role) VALUES (?, ?, ?, ?) ON CONFLICT(ProjectID, Issuer, Subject) DO UPDATE SET Role = excluded.Role`, tenant("project_memberships"))
	if _, err := tx.ExecContext(ctx, query, projectID, issuer, subject, string(role)); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectRole returns an explicit role grant, or an empty role when none exists.
func ProjectRole(ctx context.Context, projectID, issuer, subject string) (access.Role, error) {
	var role access.Role
	query := fmt.Sprintf(`SELECT Role FROM %s WHERE ProjectID = ? AND Issuer = ? AND Subject = ?`, tenant("project_memberships"))
	err := db.QueryRowContext(ctx, query, projectID, issuer, subject).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// ListProjectMembers returns the explicit grants for a project.
func ListProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	if _, err := GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`SELECT Issuer, Subject, Role FROM %s WHERE ProjectID = ? ORDER BY Issuer, Subject`, tenant("project_memberships"))
	rows, err := db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []ProjectMember{}
	for rows.Next() {
		var member ProjectMember
		if err := rows.Scan(&member.Issuer, &member.Subject, &member.Role); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// RevokeProjectRole removes a project's explicit role grant.
func RevokeProjectRole(ctx context.Context, projectID, issuer, subject string) error {
	if !validGroupText(issuer, 2048) || !validGroupText(subject, 255) {
		return ErrInvalidProject
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := projectGroup(ctx, tx, projectID); err != nil {
		return err
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE ProjectID = ? AND Issuer = ? AND Subject = ?`, tenant("project_memberships"))
	if _, err := tx.ExecContext(ctx, query, projectID, issuer, subject); err != nil {
		return err
	}
	return tx.Commit()
}

func projectGroup(ctx context.Context, queryer rowQuerier, projectID string) (string, error) {
	var groupID string
	query := fmt.Sprintf(`SELECT GroupID FROM %s WHERE ID = ?`, tenant("team_projects"))
	err := queryer.QueryRowContext(ctx, query, projectID).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	return groupID, err
}
