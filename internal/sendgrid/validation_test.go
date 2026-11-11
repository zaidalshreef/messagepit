package sendgrid

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/storage"
)

func invalidSendGridPayloads() map[string]map[string]any {
	return map[string]map[string]any{
		"newline in header name":        {"headers": map[string]string{"X-Test\r\nBcc": "hidden@example.test"}},
		"colon in header name":          {"headers": map[string]string{"X-Test: Bcc": "hidden@example.test"}},
		"space in header name":          {"headers": map[string]string{"X Test": "value"}},
		"control in header value":       {"headers": map[string]string{"X-Test": "value\x00hidden"}},
		"duplicate correlation headers": {"headers": map[string]string{"X-MessagePit-Correlation-ID": "one", "x-messagepit-correlation-id": "two"}},
		"duplicate header names":        {"headers": map[string]string{"X-Trace": "one", "x-trace": "two"}},
		"reserved from header":          {"headers": map[string]string{"From": "spoofed@example.test"}},
		"reserved reply-to":             {"headers": map[string]string{"Reply-To": "spoofed@example.test"}},
		"reserved DKIM signature":       {"headers": map[string]string{"dKiM-Signature": "spoofed"}},
		"reserved provider id":          {"headers": map[string]string{"X-SG-ID": "spoofed"}},
		"reserved bcc header":           {"headers": map[string]string{"bCc": "hidden@example.test"}},
		"reserved content type":         {"headers": map[string]string{"content-TYPE": "text/html"}},
		"injected content type":         {"content": []map[string]string{{"type": "text/plain\r\nBcc: hidden@example.test", "value": "body"}}},
		"control in notification id":    {"custom_args": map[string]string{"notification_id": "value\x00hidden"}},
		"control in scenario":           {"custom_args": map[string]string{"mp_scenario": "bounce\x01hidden"}},
		"control in personalization argument": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test"}}, "custom_args": map[string]string{"notification_id": "value\x00hidden"}},
		}},
		"invalid media type": {"content": []map[string]string{{"type": "not-a-media-type", "value": "body"}}},
		"invalid later part": {"content": []map[string]string{
			{"type": "text/plain", "value": "body"}, {"type": "text/html\nX-Injected: yes", "value": "body"},
		}},
		"invalid to address": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "not-an-address"}}},
		}},
		"invalid later to address": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test"}, {"email": "not-an-address"}}},
		}},
		"invalid later personalization": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test"}}},
			{"to": []map[string]string{{"email": "not-an-address"}}},
		}},
		"invalid cc address": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test"}}, "cc": []map[string]string{{"email": "not-an-address"}}},
		}},
		"invalid bcc address": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test"}}, "bcc": []map[string]string{{"email": "not-an-address"}}},
		}},
		"newline in address": {"personalizations": []map[string]any{
			{"to": []map[string]string{{"email": "first@example.test\r\nBcc: hidden@example.test"}}},
		}},
	}
}

func TestCreateMessageRejectsUnsafeInputBeforeStorage(t *testing.T) {
	setup()
	defer storage.Close()
	for name, fields := range invalidSendGridPayloads() {
		t.Run(name, func(t *testing.T) {
			response := makeRequest(t, validPayload(t, fields), "")
			if response.Code != http.StatusBadRequest {
				t.Errorf("unsafe MIME input returned %d, want 400", response.Code)
			} else {
				assertJSONError(t, response)
			}
		})
	}
	if messages := listMessages(t); len(messages) != 0 {
		t.Fatalf("unsafe requests stored %d messages", len(messages))
	}
}

func TestCreateMessagePreservesValidCustomHeadersAndMediaParameters(t *testing.T) {
	setup()
	defer storage.Close()
	payload := validPayload(t, map[string]any{
		"headers": map[string]string{"X-Trace_Id": "test-value"},
		"content": []map[string]string{{"type": "text/plain; charset=utf-8", "value": "Hello"}},
	})
	response := makeRequest(t, payload, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("valid input returned %d: %s", response.Code, response.Body.String())
	}
	messages := listMessages(t)
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	raw, err := storage.GetMessageRaw(messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("X-Trace_Id") != "test-value" {
		t.Fatal("valid custom header was not preserved")
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" || !strings.EqualFold(params["charset"], "utf-8") {
		t.Fatalf("invalid stored content type: %q, %v", message.Header.Get("Content-Type"), err)
	}
}

func TestCreateMessagePreservesMultipartContentTypes(t *testing.T) {
	setup()
	defer storage.Close()
	payload := validPayload(t, map[string]any{
		"content": []map[string]string{
			{"type": "text/plain; charset=utf-8", "value": "plain body"},
			{"type": "text/html; charset=utf-8", "value": "<p>html body</p>"},
		},
	})
	response := makeRequest(t, payload, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("valid multipart input returned %d", response.Code)
	}
	messages := listMessages(t)
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	raw, err := storage.GetMessageRaw(messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("invalid multipart content type: %q %v", message.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	for _, expected := range []struct{ mediaType, body string }{{"text/plain", "plain body"}, {"text/html", "<p>html body</p>"}} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || partType != expected.mediaType || !strings.EqualFold(partParams["charset"], "utf-8") {
			t.Fatalf("invalid part content type: %q %v", part.Header.Get("Content-Type"), err)
		}
		body, err := io.ReadAll(part)
		if err != nil || strings.TrimSpace(string(body)) != expected.body {
			t.Fatalf("unexpected multipart body %q: %v", body, err)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("unexpected additional multipart part: %v", err)
	}
}

func TestScopedSendGridRejectsUnsafeInputWithoutCapture(t *testing.T) {
	setup()
	defer storage.Close()
	previous := config.OIDC
	config.OIDC = oidcauth.Config{Issuer: "https://id.example"}
	defer func() { config.OIDC = previous }()
	ctx := context.Background()
	group, err := storage.CreateGroup(ctx, "Validation")
	if err != nil {
		t.Fatal(err)
	}
	project, err := storage.CreateProject(ctx, group.ID, "Validation")
	if err != nil {
		t.Fatal(err)
	}
	envs, err := storage.ListProjectEnvironments(ctx, project.ID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("environment: %+v %v", envs, err)
	}
	_, secret, err := storage.IssueServiceToken(ctx, envs[0].ID, "test", access.Developer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/v3/mail/send", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set(storage.CorrelationHeader, "validation-flow")
		w := httptest.NewRecorder()
		CreateMessage(w, r)
		return w
	}
	for name, fields := range invalidSendGridPayloads() {
		t.Run(name, func(t *testing.T) {
			response := request(validPayload(t, fields))
			if response.Code != http.StatusBadRequest {
				t.Errorf("unsafe scoped input returned %d, want 400", response.Code)
			}
		})
	}
	messages, err := storage.ListEnvironmentMessages(envs[0].ID, 0, 0, 100)
	if err != nil || len(messages) != 0 {
		t.Fatalf("unsafe requests stored scoped messages: %d %v", len(messages), err)
	}
	records, err := storage.ListCorrelatedRecords(ctx, envs[0].ID, "validation-flow")
	if err != nil || len(records) != 0 {
		t.Fatalf("unsafe requests stored correlated records: %+v %v", records, err)
	}
	response := request(validPayload(t, map[string]any{"headers": map[string]string{"X-Trace": "valid"}}))
	if response.Code != http.StatusAccepted {
		var body any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		t.Fatalf("valid scoped request returned %d: %v", response.Code, body)
	}
	records, err = storage.ListCorrelatedRecords(ctx, envs[0].ID, "validation-flow")
	if err != nil || len(records) != 1 {
		t.Fatalf("valid request lost correlation: %+v %v", records, err)
	}
}

func TestCreateMessageFormatsDisplayNamesWithSpecialCharacters(t *testing.T) {
	setup()
	defer storage.Close()
	payload := validPayload(t, map[string]any{
		"from": map[string]string{"email": "sender@example.test", "name": "Doe, Jane"},
		"personalizations": []map[string]any{{
			"to": []map[string]string{{"email": "recipient@example.test", "name": "QA: Team"}},
			"cc": []map[string]string{{"email": "copy@example.test", "name": "Copy, Team"}},
		}},
	})
	response := makeRequest(t, payload, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("valid named addresses returned %d", response.Code)
	}
	messages := listMessages(t)
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	message, err := storage.ReadMessage(messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.From == nil || message.From.Name != "Doe, Jane" || message.From.Address != "sender@example.test" {
		t.Fatalf("incorrect sender: %+v", message.From)
	}
	if len(message.To) != 1 || message.To[0].Name != "QA: Team" || message.To[0].Address != "recipient@example.test" {
		t.Fatalf("incorrect recipient: %+v", message.To)
	}
	if len(message.Cc) != 1 || message.Cc[0].Name != "Copy, Team" || message.Cc[0].Address != "copy@example.test" {
		t.Fatalf("incorrect copy recipient: %+v", message.Cc)
	}
}
