package storage

import (
	"sync"
	"testing"
	"time"
)

func TestMailboxStatsBroadcastCoalescesConcurrentCalls(t *testing.T) {
	setup("notifications_test")
	defer Close()
	previous := BroadcastFunc
	defer func() { BroadcastFunc = previous }()
	received := make(chan any, 1)
	BroadcastFunc = func(kind string, value any) {
		if kind == "stats" {
			received <- value
		}
	}
	var calls sync.WaitGroup
	for i := 0; i < 20; i++ {
		calls.Add(1)
		go func() { defer calls.Done(); BroadcastMailboxStats() }()
	}
	calls.Wait()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("mailbox statistics were not broadcast")
	}
	select {
	case <-received:
		t.Fatal("concurrent calls produced duplicate broadcasts")
	default:
	}
}
