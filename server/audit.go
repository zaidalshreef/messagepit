package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/gorilla/mux"
)

type auditWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *auditWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

// auditMutation records authenticated management requests without bodies or secrets.
func auditMutation(action string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			handler(w, r)
			return
		}
		principal, user := oidcauth.CurrentPrincipal(r.Context())
		service, client := oidcauth.CurrentServicePrincipal(r.Context())
		if !user && !client {
			handler(w, r)
			return
		}
		recorder := &auditWriter{ResponseWriter: w}
		handler(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		vars := mux.Vars(r)
		resource := vars["id"]
		if vars["record"] != "" {
			resource = vars["record"]
		} else if vars["token"] != "" {
			resource = vars["token"]
		} else if vars["username"] != "" {
			resource = vars["username"]
		}
		event := storage.AuditEvent{
			Issuer: principal.Issuer, Subject: principal.Subject, ServiceTokenID: service.ID,
			Action: action + "." + strings.ToLower(r.Method), EnvironmentID: auditID(vars["env"]),
			ResourceID: auditID(resource), Status: recorder.status,
		}
		writeAuditEvent(event)
	}
}

func auditID(value string) string {
	if len(value) > 255 {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return value
}

func writeAuditEvent(event storage.AuditEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := storage.AddAuditEvent(ctx, event); err != nil {
		logger.Log().Error("[audit] failed to record an event")
	}
}
