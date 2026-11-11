package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/coreydaley/messagepit/internal/scopedevents"
)

var ErrInvalidRetention = errors.New("invalid retention policy")

// EnvironmentRetention returns the maximum age in seconds, or zero if disabled.
func EnvironmentRetention(ctx context.Context, environmentID string) (int64, error) {
	if _, err := GetProjectEnvironment(ctx, environmentID); err != nil {
		return 0, err
	}
	var seconds int64
	err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT MaxAgeSeconds FROM %s WHERE EnvironmentID = ?`, tenant("environment_retention")), environmentID).Scan(&seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seconds, err
}

// SetEnvironmentRetention opts an environment into cleanup or disables it with zero.
func SetEnvironmentRetention(ctx context.Context, environmentID string, seconds int64) error {
	if seconds != 0 && (seconds < 3600 || seconds > 365*24*3600) {
		return ErrInvalidRetention
	}
	if _, err := GetProjectEnvironment(ctx, environmentID); err != nil {
		return err
	}
	if seconds == 0 {
		_, err := db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE EnvironmentID = ?`, tenant("environment_retention")), environmentID)
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s (EnvironmentID, MaxAgeSeconds) VALUES (?, ?) ON CONFLICT(EnvironmentID) DO UPDATE SET MaxAgeSeconds = excluded.MaxAgeSeconds`, tenant("environment_retention"))
	_, err := db.ExecContext(ctx, query, environmentID, seconds)
	return err
}

// PruneEnvironmentRecords deletes bounded batches according to each policy.
// Every record and its associated data are removed in one transaction.
func PruneEnvironmentRecords(ctx context.Context, now time.Time) error {
	query := fmt.Sprintf(`SELECT EnvironmentID, MaxAgeSeconds FROM %s`, tenant("environment_retention"))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	type policy struct {
		id      string
		seconds int64
	}
	var policies []policy
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.id, &p.seconds); err != nil {
			rows.Close()
			return err
		}
		policies = append(policies, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range policies {
		cutoff := now.Add(-time.Duration(p.seconds) * time.Second).UnixMilli()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var current int64
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT MaxAgeSeconds FROM %s WHERE EnvironmentID = ?`, tenant("environment_retention")), p.id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current != p.seconds) {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		changed, err := pruneEnvironmentBatch(ctx, tx, p.id, cutoff)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if changed {
			scopedevents.Notify(p.id)
		}
	}
	return nil
}

func pruneEnvironmentBatch(ctx context.Context, tx *sql.Tx, environmentID string, cutoff int64) (bool, error) {
	changed := false
	for _, record := range []struct {
		kind, table string
		related     []string
	}{
		{"email", "mailbox", []string{"mailbox_data", "message_tags"}},
		{"sms", "sms_mailbox", nil},
		{"webhook", "webhook_requests", nil},
	} {
		selectIDs := fmt.Sprintf(`SELECT m.ID FROM %s m WHERE %s AND m.Created < ? ORDER BY m.Created LIMIT 500`, tenant(record.table), ownershipPredicate("m.ID"))
		args := []any{record.kind, LegacyEnvironmentID, environmentID, cutoff}
		for _, table := range append(record.related, record.table) {
			query := fmt.Sprintf(`DELETE FROM %s WHERE ID IN (%s)`, tenant(table), selectIDs)
			result, err := tx.ExecContext(ctx, query, args...)
			if err != nil {
				return false, err
			}
			if table == record.table {
				count, err := result.RowsAffected()
				if err != nil {
					return false, err
				}
				changed = changed || count > 0
			}
		}
	}
	return changed, nil
}
