package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreydaley/messagepit/internal/access"
	"github.com/lithammer/shortuuid/v4"
)

var (
	ErrInvalidToken        = errors.New("invalid or expired service token")
	ErrInvalidTokenRequest = errors.New("invalid service token request")
	ErrTokenNotFound       = errors.New("service token not found")
)

// ServiceToken contains token metadata; the secret is returned only when issued.
type ServiceToken struct {
	ID            string      `json:"id"`
	EnvironmentID string      `json:"environment_id"`
	Name          string      `json:"name"`
	Role          access.Role `json:"role"`
	Created       time.Time   `json:"created_at"`
	Expires       time.Time   `json:"expires_at"`
	Revoked       bool        `json:"revoked"`
}

// IssueServiceToken creates a short-lived read or developer token for one environment.
func IssueServiceToken(ctx context.Context, environmentID, name string, role access.Role, ttl time.Duration) (ServiceToken, string, error) {
	name = strings.TrimSpace(name)
	if !validGroupText(name, 100) || (role != access.Viewer && role != access.Developer) || ttl < time.Minute || ttl > 24*time.Hour {
		return ServiceToken{}, "", ErrInvalidTokenRequest
	}
	if _, err := GetProjectEnvironment(ctx, environmentID); err != nil {
		return ServiceToken{}, "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return ServiceToken{}, "", err
	}
	created := time.Now().UTC().Truncate(time.Millisecond)
	token := ServiceToken{ID: shortuuid.New(), EnvironmentID: environmentID, Name: name, Role: role, Created: created, Expires: created.Add(ttl)}
	hash := sha256.Sum256(secret[:])
	query := fmt.Sprintf(`INSERT INTO %s (ID, EnvironmentID, Name, Role, SecretHash, Created, Expires) VALUES (?, ?, ?, ?, ?, ?, ?)`, tenant("service_tokens"))
	if _, err := db.ExecContext(ctx, query, token.ID, environmentID, name, string(role), hex.EncodeToString(hash[:]), token.Created.UnixMilli(), token.Expires.UnixMilli()); err != nil {
		return ServiceToken{}, "", err
	}
	return token, "mpt_" + token.ID + "." + base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

// AuthenticateServiceToken validates a token and returns its restricted metadata.
func AuthenticateServiceToken(ctx context.Context, value string) (ServiceToken, error) {
	id, encoded, ok := strings.Cut(value, ".")
	if !ok || !strings.HasPrefix(id, "mpt_") || len(id) <= len("mpt_") {
		return ServiceToken{}, ErrInvalidToken
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(secret) != 32 {
		return ServiceToken{}, ErrInvalidToken
	}
	id = strings.TrimPrefix(id, "mpt_")
	token, hash, err := activeServiceToken(ctx, id)
	if err != nil {
		return ServiceToken{}, err
	}
	actual := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare([]byte(hash), []byte(hex.EncodeToString(actual[:]))) != 1 {
		return ServiceToken{}, ErrInvalidToken
	}
	return token, nil
}

// ActiveServiceToken rechecks an already authenticated connection's token for
// revocation and expiry. The ID alone must never authenticate a new connection.
func ActiveServiceToken(ctx context.Context, id string) (ServiceToken, error) {
	token, _, err := activeServiceToken(ctx, id)
	return token, err
}

func activeServiceToken(ctx context.Context, id string) (ServiceToken, string, error) {
	query := fmt.Sprintf(`SELECT ID, EnvironmentID, Name, Role, SecretHash, Created, Expires, Revoked FROM %s WHERE ID = ?`, tenant("service_tokens"))
	var token ServiceToken
	var hash string
	var created, expires int64
	var revoked int
	err := db.QueryRowContext(ctx, query, id).Scan(&token.ID, &token.EnvironmentID, &token.Name, &token.Role, &hash, &created, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceToken{}, "", ErrInvalidToken
	}
	if err != nil {
		return ServiceToken{}, "", err
	}
	if revoked != 0 || time.Now().UnixMilli() >= expires || (token.Role != access.Viewer && token.Role != access.Developer) {
		return ServiceToken{}, "", ErrInvalidToken
	}
	token.Created, token.Expires = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	return token, hash, nil
}

// ListServiceTokens returns metadata without token secrets.
func ListServiceTokens(ctx context.Context, environmentID string) ([]ServiceToken, error) {
	query := fmt.Sprintf(`SELECT ID, EnvironmentID, Name, Role, Created, Expires, Revoked FROM %s WHERE EnvironmentID = ? ORDER BY Created DESC, ID`, tenant("service_tokens"))
	rows, err := db.QueryContext(ctx, query, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []ServiceToken{}
	for rows.Next() {
		var token ServiceToken
		var created, expires int64
		var revoked int
		if err := rows.Scan(&token.ID, &token.EnvironmentID, &token.Name, &token.Role, &created, &expires, &revoked); err != nil {
			return nil, err
		}
		token.Created, token.Expires, token.Revoked = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC(), revoked != 0
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeServiceToken revokes a token only within its owning environment.
func RevokeServiceToken(ctx context.Context, environmentID, id string) error {
	query := fmt.Sprintf(`UPDATE %s SET Revoked = 1 WHERE ID = ? AND EnvironmentID = ?`, tenant("service_tokens"))
	result, err := db.ExecContext(ctx, query, id, environmentID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrTokenNotFound
	}
	return nil
}
