package apiv1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/mux"
)

func groupPrincipal(w http.ResponseWriter, r *http.Request, admin bool) (oidcauth.Principal, bool) {
	principal, ok := oidcauth.CurrentPrincipal(r.Context())
	if !ok {
		http.NotFound(w, r)
		return oidcauth.Principal{}, false
	}
	if admin && !principal.Admin {
		http.Error(w, "Administrator access required", http.StatusForbidden)
		return oidcauth.Principal{}, false
	}
	return principal, true
}

func groupJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func groupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrGroupNotFound):
		http.Error(w, "Group not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrGroupExists):
		http.Error(w, "Group already exists", http.StatusConflict)
	case errors.Is(err, storage.ErrGroupHasProjects):
		http.Error(w, "Group contains projects", http.StatusConflict)
	case errors.Is(err, storage.ErrProjectNotFound):
		http.Error(w, "Project not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrEnvironmentNotFound):
		http.Error(w, "Environment not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrProjectExists), errors.Is(err, storage.ErrEnvironmentExists):
		http.Error(w, "Project or environment already exists", http.StatusConflict)
	case errors.Is(err, storage.ErrGroupMembershipRequired):
		http.Error(w, "Group membership is required", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidProject):
		http.Error(w, "Invalid project or role", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidTokenRequest):
		http.Error(w, "Invalid service token request", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidRetention):
		http.Error(w, "Invalid retention policy", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidCorrelationID):
		http.Error(w, "Invalid correlation ID", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidCorrelationPage):
		http.Error(w, "Invalid page", http.StatusBadRequest)
	case errors.Is(err, storage.ErrTokenNotFound):
		http.Error(w, "Service token not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrRecordNotFound):
		http.Error(w, "Captured record not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrInvalidGroup):
		http.Error(w, "Invalid group or member", http.StatusBadRequest)
	case errors.Is(err, storage.ErrInvalidLocalUser):
		http.Error(w, "Username must use 3–64 lowercase letters, digits, dots, underscores or hyphens; password must be 12–72 bytes", http.StatusBadRequest)
	case errors.Is(err, storage.ErrLocalUserExists):
		http.Error(w, "Username already exists", http.StatusConflict)
	case errors.Is(err, storage.ErrLocalUserNotFound):
		http.Error(w, "Local user not found", http.StatusNotFound)
	case errors.Is(err, storage.ErrLocalAdministrator):
		http.Error(w, "Cannot disable the local administrator", http.StatusConflict)
	case errors.Is(err, storage.ErrLocalCredentials):
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
	default:
		http.Error(w, "Unable to process group request", http.StatusInternalServerError)
	}
}

func decodeGroupJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

// CurrentUser returns the identity associated with the active OIDC session.
func CurrentUser(w http.ResponseWriter, r *http.Request) {
	principal, ok := groupPrincipal(w, r, false)
	if ok {
		groupJSON(w, http.StatusOK, principal)
	}
}

// AuditEvents lists recent management events for system administrators.
func AuditEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := groupPrincipal(w, r, true); !ok {
		return
	}
	start, _, limit := getStartLimit(r)
	events, err := storage.ListAuditEvents(r.Context(), start, limit)
	if err != nil {
		groupError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	groupJSON(w, http.StatusOK, struct {
		Events []storage.AuditEvent `json:"events"`
	}{events})
}

// Groups lists visible groups or creates a group for an administrator.
func Groups(w http.ResponseWriter, r *http.Request) {
	principal, ok := groupPrincipal(w, r, r.Method == http.MethodPost)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		groups, err := storage.ListGroups(r.Context(), principal.Issuer, principal.Subject, principal.Admin)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Groups []storage.Group `json:"groups"`
		}{groups})
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	group, err := storage.CreateGroup(r.Context(), input.Name)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusCreated, group)
}

// DeleteGroup removes a group and its memberships.
func DeleteGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := groupPrincipal(w, r, true); !ok {
		return
	}
	if err := storage.DeleteGroup(r.Context(), mux.Vars(r)["id"]); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GroupMembers lists members or updates membership for an administrator.
func GroupMembers(w http.ResponseWriter, r *http.Request) {
	principal, ok := groupPrincipal(w, r, r.Method != http.MethodGet)
	if !ok {
		return
	}
	id := mux.Vars(r)["id"]
	if r.Method == http.MethodGet {
		if !principal.Admin {
			member, err := storage.GroupMemberExists(r.Context(), id, principal.Issuer, principal.Subject)
			if err != nil {
				groupError(w, err)
				return
			}
			if !member {
				groupError(w, storage.ErrGroupNotFound)
				return
			}
		}
		members, err := storage.ListGroupMembers(r.Context(), id)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Members []storage.GroupMember `json:"members"`
		}{members})
		return
	}
	var input struct {
		Subject string `json:"subject"`
		Issuer  string `json:"issuer"`
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
		err = storage.AddGroupMember(r.Context(), id, principal.Issuer, input.Subject)
	} else {
		issuer := input.Issuer
		if issuer == "" {
			issuer = principal.Issuer
		}
		err = storage.RemoveGroupMember(r.Context(), id, issuer, input.Subject)
	}
	if err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
