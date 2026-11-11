package twilio

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/sirupsen/logrus"
)

type callbackLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *callbackLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *callbackLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestSMSCallbackLogsDoNotExposeEndpointSecrets(t *testing.T) {
	var logs callbackLogBuffer
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(&logs)
	log.SetLevel(logrus.DebugLevel)
	defer func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "http://", "http://private-user:private-password@", 1) + "/private-path?token=private-query"
	postStatusCallback("sid", "account", "+155501", "+155502", endpoint, statusStep{Status: "failed"})
	postStatusCallback("sid", "account", "+155501", "+155502", "http://127.0.0.1:0/private-path?token=private-query", statusStep{Status: "failed"})
	postStatusCallback("sid", "account", "+155501", "+155502", "://private-path?token=private-query", statusStep{Status: "failed"})
	output := logs.String()
	for _, sensitive := range []string{"private-user", "private-password", "private-path", "private-query"} {
		if strings.Contains(output, sensitive) {
			t.Fatal("callback log exposed endpoint credentials or data")
		}
	}
	if !strings.Contains(output, "502") || !strings.Contains(output, "failed") {
		t.Fatal("callback log lost the response status or delivery state")
	}
}
