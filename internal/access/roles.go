// Package access defines the permissions granted by project roles.
package access

// Role is an explicitly assigned project role.
type Role string

const (
	Viewer    Role = "viewer"
	Developer Role = "developer"
	Admin     Role = "admin"
)

// Permission describes an operation within one project.
type Permission string

const (
	ReadMessages  Permission = "read_messages"
	WriteMessages Permission = "write_messages"
	ManageMembers Permission = "manage_members"
	ManageProject Permission = "manage_project"
)

// Valid reports whether the role can be assigned.
func (r Role) Valid() bool {
	return r == Viewer || r == Developer || r == Admin
}

// Allows denies unknown roles and permissions by default.
func (r Role) Allows(permission Permission) bool {
	switch permission {
	case ReadMessages:
		return r == Viewer || r == Developer || r == Admin
	case WriteMessages:
		return r == Developer || r == Admin
	case ManageMembers, ManageProject:
		return r == Admin
	default:
		return false
	}
}
