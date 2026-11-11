package apiv1

import (
	"net/http"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/mux"
)

// LocalUsers lists or creates accounts without granting group or project access.
func LocalUsers(w http.ResponseWriter, r *http.Request) {
	if !config.LocalAuth {
		http.NotFound(w, r)
		return
	}
	if _, ok := groupPrincipal(w, r, true); !ok {
		return
	}
	if r.Method == http.MethodGet {
		users, err := storage.ListLocalUsers(r.Context())
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Users []storage.LocalUser `json:"users"`
		}{users})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	if err := storage.CreateLocalUser(r.Context(), input.Username, input.Password); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// DisableLocalUser removes a local user's access and all group and project grants.
func DisableLocalUser(w http.ResponseWriter, r *http.Request) {
	if !config.LocalAuth {
		http.NotFound(w, r)
		return
	}
	if _, ok := groupPrincipal(w, r, true); !ok {
		return
	}
	if err := storage.DisableLocalUser(r.Context(), mux.Vars(r)["username"]); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResetLocalPassword changes another user's password and invalidates their sessions.
func ResetLocalPassword(w http.ResponseWriter, r *http.Request) {
	if !config.LocalAuth {
		http.NotFound(w, r)
		return
	}
	if _, ok := groupPrincipal(w, r, true); !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	if err := storage.ResetLocalPassword(r.Context(), mux.Vars(r)["username"], input.Password); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LocalPassword changes the current user's password and invalidates their sessions.
func LocalPassword(w http.ResponseWriter, r *http.Request) {
	if !config.LocalAuth {
		http.NotFound(w, r)
		return
	}
	principal, ok := groupPrincipal(w, r, false)
	if !ok {
		return
	}
	if principal.Issuer != oidcauth.LocalIssuer {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	if err := storage.ChangeLocalPassword(r.Context(), principal, input.Current, input.New); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
