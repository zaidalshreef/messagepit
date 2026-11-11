package smtpd

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSMTPDebugLogsRedactMalformedAuthentication(t *testing.T) {
	previousDebug := Debug
	Debug = true
	defer func() { Debug = previousDebug }()
	var mu sync.Mutex
	var lines []string
	server := &Server{
		Hostname: "localhost", DisableReverseDNS: true,
		LogRead:  func(_, _, line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() },
		LogWrite: func(_, _, _ string) {},
	}
	client, connection := net.Pipe()
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); server.newSession(connection).serve() }()
	defer func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("SMTP session did not stop")
		}
	}()
	reader := bufio.NewReader(client)
	read := func(want string) {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, want) {
			t.Fatalf("SMTP response %q, want %q: %v", line, want, err)
		}
	}
	read("220")
	for _, item := range []struct{ command, response string }{
		{"AUTH\tPLAIN private-malformed-token", "500"},
		{"aUtH-unsupported private-unsupported-token", "500"},
		{"AU\x00TH private-broken-token", "500"},
		{"private-bare-token", "500"},
		{"AUTH PLAIN private-standard-token", "502"},
		{"NOOP", "250"},
		{"QUIT", "221"},
	} {
		if _, err := fmt.Fprintf(client, "%s\r\n", item.command); err != nil {
			t.Fatal(err)
		}
		read(item.response)
	}
	mu.Lock()
	logged := strings.Join(lines, "\n")
	mu.Unlock()
	for _, secret := range []string{"private-malformed-token", "private-unsupported-token", "private-broken-token", "private-bare-token", "private-standard-token"} {
		if strings.Contains(logged, secret) {
			t.Fatal("SMTP debug logs exposed authentication data")
		}
	}
	if !strings.Contains(logged, "NOOP") {
		t.Fatal("ordinary SMTP commands are no longer logged")
	}
}
