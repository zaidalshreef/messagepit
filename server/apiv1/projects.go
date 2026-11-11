package apiv1

import (
	"net/http"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/mux"
)

func projectPrincipal(w http.ResponseWriter, r *http.Request, permission access.Permission) (oidcauth.Principal, string, bool) {
	principal, ok := groupPrincipal(w, r, false)
	if !ok {
		return oidcauth.Principal{}, "", false
	}
	id := mux.Vars(r)["id"]
	if !principal.Admin {
		role, err := storage.ProjectRole(r.Context(), id, principal.Issuer, principal.Subject)
		if err != nil {
			groupError(w, err)
			return oidcauth.Principal{}, "", false
		}
		if !role.Allows(permission) {
			groupError(w, storage.ErrProjectNotFound)
			return oidcauth.Principal{}, "", false
		}
	}
	return principal, id, true
}

// GroupProjects lists explicitly granted projects or creates a project for an administrator.
func GroupProjects(w http.ResponseWriter, r *http.Request) {
	principal, ok := groupPrincipal(w, r, r.Method == http.MethodPost)
	if !ok {
		return
	}
	groupID := mux.Vars(r)["id"]
	if r.Method == http.MethodGet {
		if principal.Admin {
			if _, err := storage.GetGroup(r.Context(), groupID); err != nil {
				groupError(w, err)
				return
			}
		} else {
			member, err := storage.GroupMemberExists(r.Context(), groupID, principal.Issuer, principal.Subject)
			if err != nil {
				groupError(w, err)
				return
			}
			if !member {
				groupError(w, storage.ErrGroupNotFound)
				return
			}
		}
		projects, err := storage.ListProjects(r.Context(), groupID, principal.Issuer, principal.Subject, principal.Admin)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Projects []storage.Project `json:"projects"`
		}{projects})
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	project, err := storage.CreateProject(r.Context(), groupID, input.Name)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusCreated, project)
}

// ProjectDetails returns project metadata only to explicitly authorized users.
func ProjectDetails(w http.ResponseWriter, r *http.Request) {
	_, id, ok := projectPrincipal(w, r, access.ReadMessages)
	if !ok {
		return
	}
	project, err := storage.GetProject(r.Context(), id)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusOK, project)
}

// ProjectEnvironments lists environments or creates one for a project administrator.
func ProjectEnvironments(w http.ResponseWriter, r *http.Request) {
	permission := access.ReadMessages
	if r.Method == http.MethodPost {
		permission = access.ManageProject
	}
	_, id, ok := projectPrincipal(w, r, permission)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		environments, err := storage.ListProjectEnvironments(r.Context(), id)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Environments []storage.ProjectEnvironment `json:"environments"`
		}{environments})
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	environment, err := storage.CreateProjectEnvironment(r.Context(), id, input.Name)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusCreated, environment)
}

// ProjectMembers lists or changes explicit project role grants.
func ProjectMembers(w http.ResponseWriter, r *http.Request) {
	permission := access.ReadMessages
	if r.Method != http.MethodGet {
		permission = access.ManageMembers
	}
	principal, id, ok := projectPrincipal(w, r, permission)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		members, err := storage.ListProjectMembers(r.Context(), id)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Members []storage.ProjectMember `json:"members"`
		}{members})
		return
	}
	var input struct {
		Issuer  string      `json:"issuer"`
		Subject string      `json:"subject"`
		Role    access.Role `json:"role"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	var err error
	if r.Method == http.MethodPost {
		if input.Issuer != "" && input.Issuer != principal.Issuer {
			http.Error(w, "Invalid issuer", http.StatusBadRequest)
			return
		}
		err = storage.GrantProjectRole(r.Context(), id, principal.Issuer, input.Subject, input.Role)
	} else {
		issuer := input.Issuer
		if issuer == "" {
			issuer = principal.Issuer
		}
		err = storage.RevokeProjectRole(r.Context(), id, issuer, input.Subject)
	}
	if err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
