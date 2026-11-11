package storage

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coreydaley/messagepit/config"
)

var bcStatsDelay atomic.Bool

// BroadcastMailboxStats broadcasts the total number of messages
// displayed to the web UI, as well as the total unread messages.
// The lookup is very fast (< 10ms / 100k messages under load).
// Rate limited to 4x per second.
func BroadcastMailboxStats() {
	broadcastStats := BroadcastFunc
	if broadcastStats == nil || !bcStatsDelay.CompareAndSwap(false, true) {
		return
	}
	database := db
	if database == nil {
		bcStatsDelay.Store(false)
		return
	}
	table := tenant("mailbox")
	version := config.Version

	go func() {
		defer bcStatsDelay.Store(false)
		time.Sleep(250 * time.Millisecond)
		var total, unread float64 // float64 also works with rqlite
		query := fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN Read = 0 THEN 1 ELSE 0 END), 0) FROM %s`, table)
		if err := database.QueryRow(query).Scan(&total, &unread); err != nil {
			return
		}
		b := struct {
			Total   uint64
			Unread  uint64
			Version string
		}{
			Total:   uint64(total),
			Unread:  uint64(unread),
			Version: version,
		}

		broadcastStats("stats", b)
	}()
}
