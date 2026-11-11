package pop3

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/auth"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/sirupsen/logrus"
)

type synchronizedLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *synchronizedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestPOP3DebugLogsDoNotExposeCredentials(t *testing.T) {
	previousDB, previousCredentials := config.Database, auth.POP3Credentials
	t.Cleanup(func() { config.Database, auth.POP3Credentials = previousDB, previousCredentials })
	config.Database = filepath.Join(t.TempDir(), "pop3.db")
	if err := storage.InitDB(); err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	const username = "private-test-user"
	const password = "private-test-password"
	const wrongPassword = "wrong-test-password"
	const authPayload = "private-auth-payload"
	const malformedPayload = "private-malformed-payload"
	if err := auth.SetPOP3Auth(username + ":" + password); err != nil {
		t.Fatal(err)
	}

	var output synchronizedLogBuffer
	log := logger.Log()
	previousLevel, previousOutput := log.GetLevel(), log.Out
	log.SetOutput(&output)
	log.SetLevel(logrus.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel); log.SetOutput(previousOutput) })

	type command struct{ line, response string }
	session := func(commands []command) {
		t.Helper()
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()
		if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); handleClient(server) }()
		defer func() {
			_ = client.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("POP3 session did not stop")
			}
		}()
		reader := bufio.NewReader(client)
		readResponse := func(want string) {
			t.Helper()
			line, err := reader.ReadString('\n')
			if err != nil || !strings.HasPrefix(line, want) {
				t.Fatalf("POP3 response %q, want %q: %v", line, want, err)
			}
		}
		readResponse("+OK")
		for _, cmd := range commands {
			if _, err := fmt.Fprintf(client, "%s\r\n", cmd.line); err != nil {
				t.Fatal(err)
			}
			readResponse(cmd.response)
		}
	}

	// Unsupported and malformed authentication commands must not leak their
	// payloads even when they are not accepted by the POP3 parser.
	session([]command{
		{"AUTH PLAIN " + authPayload, "-ERR"},
		{"pAsS\t" + malformedPayload, "-ERR"},
		{"USER " + username, "+OK"},
		{"pass " + wrongPassword, "-ERR"},
		{"QUIT", "+OK"},
	})
	session([]command{
		{"USER " + username, "+OK"},
		{"PASS " + password, "+OK"},
		{"NOOP", "+OK"},
		{"QUIT", "+OK"},
	})

	logs := output.String()
	for _, value := range []string{username, password, wrongPassword, authPayload, malformedPayload} {
		if strings.Contains(logs, value) {
			t.Fatal("POP3 debug logs exposed authentication data")
		}
	}
	for _, value := range []string{"received: USER", "received: PASS", "received: NOOP", "failed login from"} {
		if !strings.Contains(logs, value) {
			t.Fatalf("POP3 debug logs did not retain useful event %q", value)
		}
	}
}
