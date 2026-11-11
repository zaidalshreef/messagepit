package smtpd

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/smtp"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func TestScopedSMTPAuthenticationAndRevocation(t *testing.T) {
	previousOIDC, previousDB, previousLogging, previousDebug := config.OIDC, config.Database, logger.NoLogging, Debug
	config.OIDC = oidcauth.Config{Issuer: "https://id.example"}
	config.Database = filepath.Join(t.TempDir(), "smtp.db")
	logger.NoLogging, Debug = true, true
	t.Cleanup(func() {
		config.OIDC, config.Database, logger.NoLogging, Debug = previousOIDC, previousDB, previousLogging, previousDebug
	})
	if err := storage.InitDB(); err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "SMTP")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Scoped")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environments: %+v %v", envs, err)
	}
	issued, secret, err := storage.IssueServiceToken(ctx, envs[0].ID, "SMTP", access.Developer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, viewer, err := storage.IssueServiceToken(ctx, envs[0].ID, "Reader", access.Viewer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	username := "mpt_" + issued.ID
	if ok, _ := authHandler(nil, "PLAIN", []byte(username), []byte(viewer), nil); ok {
		t.Fatal("viewer token authenticated for SMTP capture")
	}
	if ok, _ := authHandler(nil, "PLAIN", []byte("wrong"), []byte(secret), nil); ok {
		t.Fatal("token authenticated with an unrelated username")
	}
	var mu sync.Mutex
	var lines []string
	server := &Server{
		Hostname: "localhost", DisableReverseDNS: true, AuthHandler: authHandler,
		AuthMechs: map[string]bool{"PLAIN": true, "CRAM-MD5": false}, AuthRequired: true,
		MsgIDHandler: mailHandler,
		LogRead:      func(_, _, line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() },
	}
	clientConn, serverConn := net.Pipe()
	go server.newSession(serverConn).serve()
	client, err := smtp.NewClient(clientConn, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", username, secret, "localhost")); err != nil {
		t.Fatalf("SMTP authentication failed: %v", err)
	}
	send := func() error {
		if err := client.Mail("sender@example.com"); err != nil {
			return err
		}
		if err := client.Rcpt("recipient@example.com"); err != nil {
			return err
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "From: sender@example.com\r\nTo: recipient@example.com\r\nSubject: Scoped SMTP\r\nX-MessagePit-Correlation-ID: smtp-flow\r\n\r\nmessage\r\n"); err != nil {
			return err
		}
		return writer.Close()
	}
	if err := send(); err != nil {
		t.Fatalf("scoped SMTP capture failed: %v", err)
	}
	if err := storage.RevokeServiceToken(ctx, envs[0].ID, issued.ID); err != nil {
		t.Fatal(err)
	}
	if err := send(); err == nil {
		t.Fatal("an open SMTP session continued to write after revocation")
	}
	_ = client.Quit()
	items, err := storage.ListEnvironmentMessages(envs[0].ID, 0, 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("scoped messages: %+v %v", items, err)
	}
	correlated, err := storage.ListCorrelatedRecords(ctx, envs[0].ID, "smtp-flow")
	if err != nil || len(correlated) != 1 || correlated[0].ID != items[0].ID || correlated[0].Kind != "messages" {
		t.Fatalf("SMTP correlation: %+v %v", correlated, err)
	}
	legacy, err := storage.ListEnvironmentMessages(storage.LegacyEnvironmentID, 0, 0, 10)
	if err != nil || len(legacy) != 0 {
		t.Fatalf("scoped message entered legacy mailbox: %+v %v", legacy, err)
	}
	mu.Lock()
	logs := strings.Join(lines, "\n")
	mu.Unlock()
	encodedAuth := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + secret))
	if strings.Contains(logs, secret) || strings.Contains(logs, encodedAuth) {
		t.Fatal("SMTP debug logs exposed the service token")
	}
}
