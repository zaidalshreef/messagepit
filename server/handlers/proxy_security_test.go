package handlers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/sirupsen/logrus"
)

type proxyLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *proxyLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *proxyLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestProxyProtectsPrivateURLsInLogs(t *testing.T) {
	previousAllow := config.AllowInternalHTTPRequests
	config.AllowInternalHTTPRequests = true
	defer func() { config.AllowInternalHTTPRequests = previousAllow }()

	var logs proxyLogBuffer
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(&logs)
	log.SetLevel(logrus.DebugLevel)
	defer func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) }()

	var cssRequests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("case") {
		case "status":
			w.WriteHeader(http.StatusBadGateway)
		case "content":
			w.Header().Set("Content-Type", "application/private-content")
			_, _ = w.Write([]byte("unsupported"))
		case "css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = fmt.Fprintf(w, "a{background:url(ftp://private-relative/private-path?token=private-query)} b{background:url(https://public.example/asset-%d)}", cssRequests.Add(1))
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("image"))
		}
	}))
	defer endpoint.Close()
	base := strings.Replace(endpoint.URL, "http://", "http://private-user:private-password@", 1) + "/private-path?token=private-query"
	invalid := "http://[private-host/private-path?token=private-query"
	assetsMutex.Lock()
	assets["inspection-test"] = MessageAssets{
		ID:      "inspection-test",
		Created: time.Now(),
		Assets:  []string{base, base + "&case=status", base + "&case=content", base + "&case=css", invalid, "http://127.0.0.1:0/private-path?token=private-query"},
	}
	assetsMutex.Unlock()
	defer func() {
		assetsMutex.Lock()
		delete(assets, "inspection-test")
		assetsMutex.Unlock()
	}()

	request := func(data string) int {
		encoded := base64.StdEncoding.EncodeToString([]byte(data))
		r := httptest.NewRequest(http.MethodGet, "/proxy?data="+url.QueryEscape(encoded), nil)
		w := httptest.NewRecorder()
		ProxyHandler(w, r)
		return w.Code
	}
	for _, tc := range []struct {
		name, data string
		status     int
	}{
		{"malformed", "private-malformed", http.StatusBadRequest},
		{"unknown asset", "inspection-test:http://private-host/private-path?token=private-query", http.StatusBadRequest},
		{"invalid URL", "inspection-test:" + invalid, http.StatusBadRequest},
		{"failed connection", "inspection-test:http://127.0.0.1:0/private-path?token=private-query", http.StatusBadRequest},
		{"upstream status", "inspection-test:" + base + "&case=status", http.StatusBadRequest},
		{"unsupported content", "inspection-test:" + base + "&case=content", http.StatusBadRequest},
		{"CSS", "inspection-test:" + base + "&case=css", http.StatusOK},
		{"image", "inspection-test:" + base, http.StatusOK},
	} {
		if got := request(tc.data); got != tc.status {
			t.Errorf("%s: got status %d, want %d", tc.name, got, tc.status)
		}
	}

	// CSS inspection extends the shared asset cache while other requests read it.
	var wg sync.WaitGroup
	statuses := make(chan int, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- request("inspection-test:" + base + "&case=css")
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatal("concurrent CSS proxying failed")
		}
	}

	output := logs.String()
	for _, secret := range []string{"private-user", "private-password", "private-host", "private-path", "private-query", "private-content", "private-relative", "private-malformed"} {
		if strings.Contains(output, secret) {
			t.Fatal("proxy log exposed message content or a private URL")
		}
	}
	if !strings.Contains(output, "502") || !strings.Contains(output, "request completed (200)") {
		t.Fatal("proxy log lost HTTP status diagnostics")
	}
}
