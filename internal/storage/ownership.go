package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/coreydaley/messagepit/internal/scopedevents"
)

var ErrRecordNotFound = errors.New("captured record not found")

// addOwnership records the environment in the same transaction as the data.
func addOwnership(ctx context.Context, tx *sql.Tx, kind, id, environmentID, correlationID string) error {
	if !ValidCorrelationID(correlationID) {
		return ErrInvalidCorrelationID
	}
	query := fmt.Sprintf(`INSERT INTO %s (Kind, RecordID, EnvironmentID) SELECT ?, ?, ID FROM %s WHERE ID = ?`, tenant("message_ownership"), tenant("project_environments"))
	result, err := tx.ExecContext(ctx, query, kind, id, environmentID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrEnvironmentNotFound
	}
	if correlationID != "" {
		query := fmt.Sprintf(`INSERT INTO %s (Kind, RecordID, CorrelationID, Created) VALUES (?, ?, ?, ?)`, tenant("capture_correlations"))
		if _, err := tx.ExecContext(ctx, query, kind, id, correlationID, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

type recordExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// saveRecord atomically saves a scoped record and its ownership, or saves a
// standalone record without changing the existing standalone workflow.
func saveRecord(ctx context.Context, environmentID, kind, id, correlationID string, save func(recordExecer) error) error {
	if environmentID == "" {
		return save(db)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := save(tx); err != nil {
		return err
	}
	if err := addOwnership(ctx, tx, kind, id, environmentID, correlationID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	scopedevents.Notify(environmentID)
	return nil
}

// ownershipPredicate treats records without an ownership row as legacy data.
func ownershipPredicate(idColumn string) string {
	return fmt.Sprintf(`COALESCE((SELECT EnvironmentID FROM %s WHERE Kind = ? AND RecordID = %s), ?) = ?`, tenant("message_ownership"), idColumn)
}

// RecordInEnvironment checks both existence and ownership without exposing other environments.
func RecordInEnvironment(ctx context.Context, kind, id, environmentID string) (bool, error) {
	var table string
	switch kind {
	case "email":
		table = tenant("mailbox")
	case "sms":
		table = tenant("sms_mailbox")
	case "webhook":
		table = tenant("webhook_requests")
	default:
		return false, ErrInvalidProject
	}
	if environmentID == "" {
		return false, ErrEnvironmentNotFound
	}
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s AS m WHERE m.ID = ? AND %s)`, table, ownershipPredicate("m.ID"))
	var exists int
	err := db.QueryRowContext(ctx, query, id, kind, LegacyEnvironmentID, environmentID).Scan(&exists)
	return exists == 1, err
}

// DeleteEnvironmentRecord deletes one record only when it belongs to the given environment.
func DeleteEnvironmentRecord(ctx context.Context, kind, id, environmentID string) error {
	var table string
	var related []string
	switch kind {
	case "email":
		table, related = "mailbox", []string{"mailbox_data", "message_tags"}
	case "sms":
		table = "sms_mailbox"
	case "webhook":
		table = "webhook_requests"
	default:
		return ErrRecordNotFound
	}
	if environmentID == "" {
		return ErrRecordNotFound
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	selection := fmt.Sprintf(`SELECT m.ID FROM %s m WHERE m.ID = ? AND %s`, tenant(table), ownershipPredicate("m.ID"))
	args := []any{id, kind, LegacyEnvironmentID, environmentID}
	for _, name := range related {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE ID IN (%s)`, tenant(name), selection), args...); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE ID IN (%s)`, tenant(table), selection), args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrRecordNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	scopedevents.Notify(environmentID)
	return nil
}
