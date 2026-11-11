package mailevents

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/sirupsen/logrus"
)

type eventLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *eventLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *eventLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestEmailCallbackLogsDoNotExposeEndpointOrRecipient(t *testing.T) {
	previousURL, previousKey := config.EmailWebhookURL, config.EmailWebhookPrivateKey()
	defer func() { config.EmailWebhookURL = previousURL; config.SetEmailWebhookPrivateKey(previousKey) }()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config.SetEmailWebhookPrivateKey(key)
	var logs eventLogBuffer
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(&logs)
	log.SetLevel(logrus.DebugLevel)
	defer func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) }()

	var status atomic.Int32
	status.Store(http.StatusBadGateway)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(int(status.Load())) }))
	defer server.Close()
	config.EmailWebhookURL = strings.Replace(server.URL, "http://", "http://private-user:private-password@", 1) + "/private-path?token=private-query"
	event := map[string]any{"event": "delivered", "email": "private-recipient@example.test"}
	post(event)
	status.Store(http.StatusOK)
	post(event)
	config.EmailWebhookURL = "http://127.0.0.1:0/private-path?token=private-query"
	post(event)
	config.EmailWebhookURL = "://private-path?token=private-query"
	post(event)
	output := logs.String()
	for _, sensitive := range []string{"private-user", "private-password", "private-path", "private-query", "private-recipient"} {
		if strings.Contains(output, sensitive) {
			t.Fatal("email callback log exposed endpoint credentials or a recipient")
		}
	}
	if !strings.Contains(output, "502") || !strings.Contains(output, "delivered") {
		t.Fatal("email callback log lost the response status or event type")
	}
}
