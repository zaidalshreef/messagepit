package scopedevents

import (
	"sync"
	"testing"
)

func TestNotificationsAreScopedAndCoalesced(t *testing.T) {
	var broker Broker
	a, cancelA, err := broker.subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	b, cancelB, err := broker.subscribe("b")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()
	broker.notify("a")
	broker.notify("a")
	select {
	case <-a:
	default:
		t.Fatal("missing a notification")
	}
	select {
	case <-a:
		t.Fatal("notifications did not coalesce")
	default:
	}
	select {
	case <-b:
		t.Fatal("notification reached a different environment")
	default:
	}
	cancelA()
	cancelA()
	broker.notify("a")
	select {
	case <-a:
		t.Fatal("cancelled subscriber received a notification")
	default:
	}
}

func TestSubscribersAreBoundedAndCanUnsubscribe(t *testing.T) {
	var broker Broker
	var cancel []func()
	for i := 0; i < maxPerEnvironment; i++ {
		_, stop, err := broker.subscribe("a")
		if err != nil {
			t.Fatal(err)
		}
		cancel = append(cancel, stop)
	}
	if _, _, err := broker.subscribe("a"); err != ErrCapacity {
		t.Fatalf("got %v, want capacity error", err)
	}
	for _, stop := range cancel {
		stop()
	}
	_, stop, err := broker.subscribe("a")
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

func TestConcurrentNotifyAndCancel(t *testing.T) {
	var broker Broker
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		_, stop, err := broker.subscribe("a")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				broker.notify("a")
			}
		}()
		go func() { defer wg.Done(); stop() }()
	}
	wg.Wait()
}
