package mailadapter

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/coreydaley/messagepit/internal/logger"
)

func LimitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
}

func BearerAuth(w http.ResponseWriter, r *http.Request, key, logPrefix string) bool {
	if key == "" {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	supplied := strings.TrimPrefix(authHeader, "Bearer ")
	if !strings.HasPrefix(authHeader, "Bearer ") ||
		subtle.ConstantTimeCompare([]byte(supplied), []byte(key)) != 1 {
		logger.Log().Warnf("%s invalid API key from %s", logPrefix, r.RemoteAddr)
		JSONError(w, http.StatusUnauthorized, "Unauthorized")
		return false
	}
	return true
}

func FormatAddress(email, name string) (string, error) {
	address, err := mail.ParseAddress(email)
	if err != nil {
		return "", fmt.Errorf("invalid email address: %s", email)
	}
	if name != "" {
		address.Name = name
		return address.String(), nil
	}
	return email, nil
}

func SanitizeHeaderValue(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

func JSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
