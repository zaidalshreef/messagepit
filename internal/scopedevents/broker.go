// Package scopedevents distributes capture change notifications by environment.
// Notifications contain no captured data; clients must fetch it through the API.
package scopedevents

import (
	"errors"
	"sync"
)

const (
	maxSubscribers    = 4096
	maxPerEnvironment = 128
)

var ErrCapacity = errors.New("too many live update subscribers")

// Broker coalesces notifications for each subscriber so a slow client cannot
// block storage or accumulate an unbounded queue.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan struct{}]struct{}
	count       int
}

var defaultBroker = &Broker{}

// Subscribe registers for changes to exactly one environment. The returned
// cancellation function is safe to call more than once.
func Subscribe(environmentID string) (<-chan struct{}, func(), error) {
	return defaultBroker.subscribe(environmentID)
}

func (b *Broker) subscribe(environmentID string) (<-chan struct{}, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count >= maxSubscribers || len(b.subscribers[environmentID]) >= maxPerEnvironment {
		return nil, nil, ErrCapacity
	}
	if b.subscribers == nil {
		b.subscribers = make(map[string]map[chan struct{}]struct{})
	}
	if b.subscribers[environmentID] == nil {
		b.subscribers[environmentID] = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	b.subscribers[environmentID][ch] = struct{}{}
	b.count++
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subscribers[environmentID], ch)
			b.count--
			if len(b.subscribers[environmentID]) == 0 {
				delete(b.subscribers, environmentID)
			}
		})
	}
	return ch, cancel, nil
}

// Notify announces a committed change to subscribers of this environment.
func Notify(environmentID string) {
	defaultBroker.notify(environmentID)
}

func (b *Broker) notify(environmentID string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers[environmentID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
