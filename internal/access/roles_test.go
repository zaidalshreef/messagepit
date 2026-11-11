package access

import "testing"

func TestProjectRoles(t *testing.T) {
	tests := []struct {
		role       Role
		permission Permission
		allowed    bool
	}{
		{Viewer, ReadMessages, true},
		{Viewer, WriteMessages, false},
		{Viewer, ManageMembers, false},
		{Developer, ReadMessages, true},
		{Developer, WriteMessages, true},
		{Developer, ManageMembers, false},
		{Developer, ManageProject, false},
		{Admin, ReadMessages, true},
		{Admin, WriteMessages, true},
		{Admin, ManageMembers, true},
		{Admin, ManageProject, true},
		{Role("unknown"), ReadMessages, false},
		{Admin, Permission("unknown"), false},
	}
	for _, tt := range tests {
		if got := tt.role.Allows(tt.permission); got != tt.allowed {
			t.Errorf("%s / %s: got %v, want %v", tt.role, tt.permission, got, tt.allowed)
		}
	}
}
