package storage

import (
	"context"
	"fmt"
	"time"
)

// AuditEvent describes an authentication or management action without request content.
type AuditEvent struct {
	ID             int64     `json:"id"`
	Created        time.Time `json:"created_at"`
	Issuer         string    `json:"issuer,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	ServiceTokenID string    `json:"service_token_id,omitempty"`
	Action         string    `json:"action"`
	EnvironmentID  string    `json:"environment_id,omitempty"`
	ResourceID     string    `json:"resource_id,omitempty"`
	Status         int       `json:"status"`
}

// AddAuditEvent records a management event without storing bodies or secrets.
func AddAuditEvent(ctx context.Context, event AuditEvent) error {
	query := fmt.Sprintf(`INSERT INTO %s (Created, Issuer, Subject, ServiceTokenID, Action, EnvironmentID, ResourceID, Status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tenant("audit_events"))
	_, err := db.ExecContext(ctx, query, time.Now().UTC().UnixMilli(), event.Issuer, event.Subject, event.ServiceTokenID, event.Action, event.EnvironmentID, event.ResourceID, event.Status)
	return err
}

// ListAuditEvents returns a bounded page of the latest events.
func ListAuditEvents(ctx context.Context, start, limit int) ([]AuditEvent, error) {
	if start < 0 {
		start = 0
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := fmt.Sprintf(`SELECT ID, Created, Issuer, Subject, ServiceTokenID, Action, EnvironmentID, ResourceID, Status FROM %s ORDER BY ID DESC LIMIT ? OFFSET ?`, tenant("audit_events"))
	rows, err := db.QueryContext(ctx, query, limit, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		var created int64
		if err := rows.Scan(&event.ID, &created, &event.Issuer, &event.Subject, &event.ServiceTokenID, &event.Action, &event.EnvironmentID, &event.ResourceID, &event.Status); err != nil {
			return nil, err
		}
		event.Created = time.UnixMilli(created).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}
