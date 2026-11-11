// Package webhookd implements the HTTP webhook capture server.
package webhookd

import (
	"io"
	"maps"
	"net"
	"net/http"
	"strings"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/coreydaley/messagepit/internal/serviceauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

const maxBodySize = 10 * 1024 * 1024 // 10MB

// CaptureHandler is a catch-all HTTP handler that captures incoming requests.
func CaptureHandler(w http.ResponseWriter, r *http.Request) {
	// Respond to CORS preflight without storing anything
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	environmentID := ""
	if config.SharedAuthEnabled() {
		token, err := serviceauth.Bearer(r)
		if err != nil {
			http.Error(w, "invalid service credential", http.StatusUnauthorized)
			return
		}
		environmentID = token.EnvironmentID
		if !storage.ValidCorrelationID(r.Header.Get(storage.CorrelationHeader)) {
			http.Error(w, "invalid correlation ID", http.StatusBadRequest)
			return
		}
	}

	body, readErr := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	_ = r.Body.Close()
	if environmentID != "" {
		if readErr != nil {
			http.Error(w, "unable to read request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxBodySize {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
	} else if len(body) > maxBodySize {
		body = body[:maxBodySize]
	}

	contentType := r.Header.Get("Content-Type")
	if i := strings.Index(contentType, ";"); i != -1 {
		contentType = strings.TrimSpace(contentType[:i])
	}

	sourceIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	headers := make(map[string][]string, len(r.Header))
	maps.Copy(headers, r.Header)
	if environmentID != "" {
		// Header names are case-insensitive, including keys supplied by middleware.
		for name := range headers {
			if strings.EqualFold(name, "Authorization") {
				delete(headers, name)
			}
		}
	}

	var err error
	if environmentID != "" {
		_, err = storage.StoreWebhookInEnvironment(r.Context(), environmentID, r.Method, r.URL.Path, r.URL.RawQuery, headers, body, contentType, sourceIP)
	} else {
		_, err = storage.StoreWebhook(r.Method, r.URL.Path, r.URL.RawQuery, headers, body, contentType, sourceIP)
	}
	if err != nil {
		logger.Log().Errorf("[webhookd] failed to store webhook: %s", err.Error())
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
