package webhook

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	changed chan struct{}
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.Buffer.Write(p)
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestWebhookLogsDoNotExposeEndpointSecrets(t *testing.T) {
	previousURL, previousRate, previousDelay := config.WebhookURL, RateLimit, Delay
	RateLimit, Delay, once, rl = 0, 0, sync.Once{}, rate.Sometimes{}
	defer func() {
		config.WebhookURL, RateLimit, Delay = previousURL, previousRate, previousDelay
		once, rl = sync.Once{}, rate.Sometimes{}
	}()
	logs := &synchronizedBuffer{changed: make(chan struct{}, 4)}
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(logs)
	log.SetLevel(logrus.DebugLevel)
	defer func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()

	waitForLog := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for !strings.Contains(logs.String(), want) {
			select {
			case <-logs.changed:
			case <-timeout:
				t.Fatalf("webhook did not log %q", want)
			}
		}
	}
	config.WebhookURL = strings.Replace(server.URL, "http://", "http://private-user:private-password@", 1) + "/private-path?token=private-query"
	Send(map[string]string{"message": "test"})
	waitForLog("502")
	config.WebhookURL = "http://127.0.0.1:0/private-path?token=private-query"
	Send(map[string]string{"message": "test"})
	waitForLog("error sending data")
	config.WebhookURL = "://private-path?token=private-query"
	Send(map[string]string{"message": "test"})
	waitForLog("[webhook] error:")
	output := logs.String()
	for _, sensitive := range []string{"private-user", "private-password", "private-path", "private-query"} {
		if strings.Contains(output, sensitive) {
			t.Fatal("webhook log exposed endpoint credentials or data")
		}
	}
}
