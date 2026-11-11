package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCorrelationTimelineIsScopedAndPaginatesBeyondIndexLimit(t *testing.T) {
	setup("correlation_timeline")
	defer Close()
	ctx := context.Background()
	group, err := CreateGroup(ctx, "Timeline")
	if err != nil {
		t.Fatal(err)
	}
	makeEnvironment := func(name string) string {
		t.Helper()
		project, err := CreateProject(ctx, group.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		environments, err := ListProjectEnvironments(ctx, project.ID)
		if err != nil || len(environments) != 1 {
			t.Fatal("project environment not created")
		}
		return environments[0].ID
	}
	env, other := makeEnvironment("A"), makeEnvironment("B")
	email := []byte("From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: Email timeline\r\nX-MessagePit-Correlation-ID: build-123\r\n\r\nEmail content\r\n")
	emailID, err := StoreInEnvironment(ctx, env, &email, nil)
	if err != nil {
		t.Fatal(err)
	}
	hookID, err := StoreWebhookInEnvironment(ctx, env, "POST", "/test-hook", "", map[string][]string{CorrelationHeader: {"build-123"}}, []byte("Webhook content"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	var firstSMS string
	for i := range 105 {
		from := "+155501"
		if i == 0 {
			from = strings.Repeat("a", 400)
		}
		id, err := StoreSMSInEnvironment(ctx, env, from, "+155502", fmt.Sprintf("SMS %03d %s", i, strings.Repeat("é", 260)), "", "build-123")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstSMS = id
		}
	}
	if _, err := StoreSMSInEnvironment(ctx, other, "private", "private", "Other project content", "", "build-123"); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for start := 0; start < 107; start += 20 {
		items, total, err := ListCorrelationTimeline(ctx, env, "build-123", start, 20)
		if err != nil || total != 107 || len(items) == 0 || len(items) > 20 {
			t.Fatalf("timeline page: total=%d length=%d error=%v", total, len(items), err)
		}
		for _, item := range items {
			if seen[item.ID] || item.Created.IsZero() || strings.Contains(item.Snippet, "Other project") {
				t.Fatal("timeline duplicated a record, omitted its date, or crossed environments")
			}
			seen[item.ID] = true
			switch item.Kind {
			case "messages":
				if item.ID != emailID || item.Title != "Email timeline" {
					t.Fatal("email timeline summary is incorrect")
				}
			case "sms":
				if len([]rune(item.Title)) > 250 || len([]rune(item.Snippet)) > 250 || (item.ID != firstSMS && item.Title != "+155501 → +155502") {
					t.Fatal("SMS timeline summary is incorrect or unbounded")
				}
			case "webhooks":
				if item.ID != hookID || item.Title != "POST /test-hook" {
					t.Fatal("webhook timeline summary is incorrect")
				}
			default:
				t.Fatal("unexpected capture kind in timeline")
			}
		}
	}
	if len(seen) != 107 || !seen[firstSMS] || !seen[emailID] || !seen[hookID] {
		t.Fatal("timeline omitted captures across its pages")
	}
	items, total, err := ListCorrelationTimeline(ctx, env, "missing", 0, 20)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatal("empty timeline returned unexpected data")
	}
	if _, _, err := ListCorrelationTimeline(ctx, env, "bad id", 0, 20); !errors.Is(err, ErrInvalidCorrelationID) {
		t.Fatal("invalid correlation ID was accepted")
	}
	for _, page := range [][2]int{{-1, 20}, {0, 0}, {0, 51}} {
		if _, _, err := ListCorrelationTimeline(ctx, env, "build-123", page[0], page[1]); !errors.Is(err, ErrInvalidCorrelationPage) {
			t.Fatal("invalid correlation page was accepted")
		}
	}
	sms, err := GetSMS(firstSMS)
	if err != nil || sms.Read {
		t.Fatal("viewing the timeline marked an SMS as read")
	}
	hook, err := GetWebhook(hookID)
	if err != nil || hook.Read {
		t.Fatal("viewing the timeline marked a webhook as read")
	}
	emails, err := ListEnvironmentMessages(env, 0, 0, 10)
	if err != nil || len(emails) != 1 || emails[0].ID != emailID || emails[0].Read {
		t.Fatal("viewing the timeline marked an email as read")
	}
	legacy, err := ListCorrelatedRecords(ctx, env, "build-123")
	if err != nil || len(legacy) != 100 {
		t.Fatal("existing correlation lookup no longer preserves its 100-record limit")
	}
}
