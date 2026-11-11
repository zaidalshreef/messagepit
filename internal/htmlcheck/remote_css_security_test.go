package htmlcheck

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/coreydaley/messagepit/internal/transport"
	"github.com/sirupsen/logrus"
)

type inspectionLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *inspectionLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *inspectionLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestStylesheetInspectionBlocksInternalAddressesAndProtectsLogs(t *testing.T) {
	previousAllow, previousBlock := config.AllowInternalHTTPRequests, config.BlockRemoteCSSAndFonts
	defer func() { config.AllowInternalHTTPRequests, config.BlockRemoteCSSAndFonts = previousAllow, previousBlock }()
	config.BlockRemoteCSSAndFonts = false

	var logs inspectionLogBuffer
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(&logs)
	log.SetLevel(logrus.DebugLevel)
	defer func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) }()

	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("failure") == "1" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte("body { color: red; }"))
	}))
	defer endpoint.Close()
	stylesheet := endpoint.URL + "/private-path?token=private-query"
	html := `<html><head><link rel="stylesheet" href="` + stylesheet + `"></head></html>`

	config.AllowInternalHTTPRequests = false
	if _, err := inlineRemoteCSS(html); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("HTML inspection reached a private address without opt-in")
	}

	config.AllowInternalHTTPRequests = true
	result, err := inlineRemoteCSS(html)
	if err != nil || !strings.Contains(result, "body { color: red; }") || requests.Load() != 1 {
		t.Fatal("HTML inspection did not include an explicitly allowed stylesheet")
	}
	if _, err := inlineRemoteCSS(`<link rel="stylesheet" href="` + stylesheet + `&amp;failure=1">`); err != nil {
		t.Fatal(err)
	}
	if _, err := inlineRemoteCSS(`<link rel="stylesheet" href="file://private-user:private-password@private-host/private-path?token=private-query">`); err != nil {
		t.Fatal(err)
	}
	config.BlockRemoteCSSAndFonts = true
	if _, err := inlineRemoteCSS(html); err != nil {
		t.Fatal(err)
	}

	output := logs.String()
	for _, secret := range []string{"private-user", "private-password", "private-host", "private-path", "private-query"} {
		if strings.Contains(output, secret) {
			t.Fatal("HTML inspection exposed a stylesheet URL or credential in logs")
		}
	}
	if !strings.Contains(output, "could not download stylesheet") || !strings.Contains(output, "unsupported stylesheet URL") {
		t.Fatal("HTML inspection lost useful failure diagnostics")
	}
}

func TestStylesheetRedirectCannotReachInternalAddress(t *testing.T) {
	previousAllow := config.AllowInternalHTTPRequests
	config.AllowInternalHTTPRequests = false
	defer func() { config.AllowInternalHTTPRequests = previousAllow }()

	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/private", http.StatusFound)
	}))
	defer redirect.Close()

	client := newSafeHTTPClient()
	tr := client.Transport.(*http.Transport)
	safeDial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		// Simulate a publicly reachable first hop without using the network.
		if address == "public.example:80" {
			return (&net.Dialer{}).DialContext(ctx, network, redirect.Listener.Addr().String())
		}
		return safeDial(ctx, network, address)
	}
	resp, err := client.Get("http://public.example/stylesheet")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, transport.ErrInternalAddress) || targetRequests.Load() != 0 {
		t.Fatal("stylesheet redirect was not blocked before reaching an internal address")
	}
}
