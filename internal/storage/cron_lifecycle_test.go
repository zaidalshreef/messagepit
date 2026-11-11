package storage

import (
	"testing"
	"time"
)

func TestCloseStopsDatabaseBackgroundWork(t *testing.T) {
	setup("cron_lifecycle")
	done := make(chan struct{})
	go func() { Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("database close waited for the next maintenance interval")
	}
	if err := Ping(); err == nil {
		t.Fatal("database remained open after Close")
	}
	Close() // Closing twice is safe.
}
