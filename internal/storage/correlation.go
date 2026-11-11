package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const CorrelationHeader = "X-MessagePit-Correlation-ID"

var (
	ErrInvalidCorrelationID   = errors.New("invalid correlation ID")
	ErrInvalidCorrelationPage = errors.New("invalid correlation page")
)

// ValidCorrelationID permits a compact, transport-safe optional identifier.
func ValidCorrelationID(value string) bool {
	if len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

type CorrelatedRecord struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// CorrelationSummary is a bounded, environment-scoped preview of a capture.
type CorrelationSummary struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Snippet string    `json:"snippet"`
	Created time.Time `json:"created"`
}

func correlationAPIKind(kind string) string {
	switch kind {
	case "email":
		return "messages"
	case "webhook":
		return "webhooks"
	default:
		return kind
	}
}

// ListCorrelatedRecords finds up to 100 records in a single environment.
func ListCorrelatedRecords(ctx context.Context, environmentID, correlationID string) ([]CorrelatedRecord, error) {
	if environmentID == "" || correlationID == "" || !ValidCorrelationID(correlationID) {
		return nil, ErrInvalidCorrelationID
	}
	query := fmt.Sprintf(`SELECT c.Kind, c.RecordID FROM %s c JOIN %s o ON o.Kind = c.Kind AND o.RecordID = c.RecordID WHERE o.EnvironmentID = ? AND c.CorrelationID = ? ORDER BY c.Created DESC, c.RecordID LIMIT 100`, tenant("capture_correlations"), tenant("message_ownership"))
	rows, err := db.QueryContext(ctx, query, environmentID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []CorrelatedRecord{}
	for rows.Next() {
		var record CorrelatedRecord
		if err := rows.Scan(&record.Kind, &record.ID); err != nil {
			return nil, err
		}
		record.Kind = correlationAPIKind(record.Kind)
		records = append(records, record)
	}
	return records, rows.Err()
}

// ListCorrelationTimeline returns a consistent page of previews from one environment.
func ListCorrelationTimeline(ctx context.Context, environmentID, correlationID string, start, limit int) ([]CorrelationSummary, int, error) {
	if environmentID == "" || correlationID == "" || !ValidCorrelationID(correlationID) {
		return nil, 0, ErrInvalidCorrelationID
	}
	if start < 0 || limit < 1 || limit > 50 {
		return nil, 0, ErrInvalidCorrelationPage
	}
	base := fmt.Sprintf(` FROM %s c
		JOIN %s o ON o.Kind = c.Kind AND o.RecordID = c.RecordID
		LEFT JOIN %s m ON c.Kind = 'email' AND m.ID = c.RecordID
		LEFT JOIN %s s ON c.Kind = 'sms' AND s.ID = c.RecordID
		LEFT JOIN %s w ON c.Kind = 'webhook' AND w.ID = c.RecordID
		WHERE o.EnvironmentID = ? AND c.CorrelationID = ? AND
		((c.Kind = 'email' AND m.ID IS NOT NULL) OR (c.Kind = 'sms' AND s.ID IS NOT NULL) OR (c.Kind = 'webhook' AND w.ID IS NOT NULL))`,
		tenant("capture_correlations"), tenant("message_ownership"), tenant("mailbox"), tenant("sms_mailbox"), tenant("webhook_requests"))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)`+base, environmentID, correlationID).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT c.Kind, c.RecordID, c.Created,
		SUBSTR(CASE c.Kind WHEN 'email' THEN COALESCE(NULLIF(m.Subject, ''), '(No subject)')
			WHEN 'sms' THEN s.FromNumber || ' → ' || s.ToNumber
			ELSE w.Method || ' ' || w.Path END, 1, 250),
		SUBSTR(CASE c.Kind WHEN 'email' THEN m.Snippet WHEN 'sms' THEN s.Body ELSE w.Snippet END, 1, 250)` + base +
		` ORDER BY c.Created DESC, c.Kind, c.RecordID LIMIT ? OFFSET ?`
	rows, err := tx.QueryContext(ctx, query, environmentID, correlationID, limit, start)
	if err != nil {
		return nil, 0, err
	}
	items := []CorrelationSummary{}
	for rows.Next() {
		var item CorrelationSummary
		var created int64
		if err := rows.Scan(&item.Kind, &item.ID, &created, &item.Title, &item.Snippet); err != nil {
			rows.Close()
			return nil, 0, err
		}
		item.Kind = correlationAPIKind(item.Kind)
		item.Created = time.UnixMilli(created).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
