package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigurationRequiresSafeCompleteEndpoint(t *testing.T) {
	for _, cfg := range []Config{
		{BaseURL: "https://ai.example/v1"},
		{BaseURL: "http://ai.example/v1", Model: "model"},
		{BaseURL: "https://user:password@ai.example/v1", Model: "model"},
		{BaseURL: "https://ai.example/v1?key=secret", Model: "model"},
		{BaseURL: "https://ai.example/v1", Model: "model", APIKey: "bad\nheader"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("unsafe AI configuration accepted: %+v", cfg)
		}
	}
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("disabled AI configuration rejected: %v", err)
	}
}

func TestSummarizeUsesConfiguredModelAndBoundsInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var input struct {
			Model    string        `json:"model"`
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "test-model" || len(input.Messages) != 2 || !strings.Contains(input.Messages[1].Content, "[Content truncated]") || len(input.Messages[1].Content) > 17000 {
			t.Errorf("unexpected AI request: %+v %v", input, err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Test summary"}}]}`))
	}))
	defer server.Close()
	cfg := Config{BaseURL: server.URL + "/v1/models", Model: "test-model", APIKey: "test-key"}
	got, err := Summarize(context.Background(), cfg, "email", strings.Repeat("x", 20000))
	if err != nil || got != "Test summary" {
		t.Fatalf("summary: %q, %v", got, err)
	}
}

func TestSummarizeDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := Summarize(context.Background(), Config{BaseURL: server.URL + "/v1", Model: "test-model", APIKey: "test-key"}, "SMS", "hello")
	if !errors.Is(err, ErrUnavailable) || redirected {
		t.Fatalf("redirect was followed: %v, %v", redirected, err)
	}
}
