package apiv1

import (
	"bytes"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/access"
	"github.com/coreydaley/messagepit/internal/ai"
	"github.com/coreydaley/messagepit/internal/html2text"
	"github.com/coreydaley/messagepit/internal/mailevents"
	"github.com/coreydaley/messagepit/internal/oidcauth"
	"github.com/coreydaley/messagepit/internal/qa"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/coreydaley/messagepit/internal/twilio"
	"github.com/gorilla/mux"
)

func environmentForRequest(w http.ResponseWriter, r *http.Request, permission access.Permission) (storage.ProjectEnvironment, bool) {
	principal, hasPrincipal := oidcauth.CurrentPrincipal(r.Context())
	service, hasService := oidcauth.CurrentServicePrincipal(r.Context())
	if !hasPrincipal && !hasService {
		http.NotFound(w, r)
		return storage.ProjectEnvironment{}, false
	}
	environment, err := storage.GetProjectEnvironment(r.Context(), mux.Vars(r)["env"])
	if err != nil {
		groupError(w, err)
		return storage.ProjectEnvironment{}, false
	}
	if hasService {
		if service.EnvironmentID != environment.ID || !service.Role.Allows(permission) {
			groupError(w, storage.ErrEnvironmentNotFound)
			return storage.ProjectEnvironment{}, false
		}
	} else if !principal.Admin {
		role, err := storage.ProjectRole(r.Context(), environment.ProjectID, principal.Issuer, principal.Subject)
		if err != nil {
			groupError(w, err)
			return storage.ProjectEnvironment{}, false
		}
		if !role.Allows(permission) {
			groupError(w, storage.ErrEnvironmentNotFound)
			return storage.ProjectEnvironment{}, false
		}
	}
	return environment, true
}

// EnvironmentSimulation reports the test delivery scenario for an authorized record.
func EnvironmentSimulation(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	var steps []qa.DeliveryStep
	switch scopedKind(r) {
	case "email":
		message, err := storage.ReadMessage(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		raw, err := storage.GetMessageRaw(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		parsed, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		scenario := parsed.Header.Get(mailevents.ScenarioHeader)
		if scenario == "" && len(message.To) > 0 && message.To[0] != nil {
			scenario = mailevents.ScenarioFor(message.To[0].Address)
		}
		for _, event := range mailevents.Sequence(scenario) {
			steps = append(steps, qa.DeliveryStep{Status: event})
		}
	case "sms":
		message, err := storage.GetSMS(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		steps = twilio.SimulatedProgression(message.To)
	default:
		http.NotFound(w, r)
		return
	}
	groupJSON(w, http.StatusOK, struct {
		Steps []qa.DeliveryStep `json:"steps"`
	}{steps})
}

// EnvironmentSearch searches email, SMS, and webhooks in one authorized environment.
func EnvironmentSearch(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" || len(query) > 512 {
		http.Error(w, "A search query of at most 512 bytes is required", http.StatusBadRequest)
		return
	}
	const limit = 20
	email, emailTotal, err := storage.SearchEnvironmentMessages(environment.ID, query, r.URL.Query().Get("timezone"), 0, 0, limit)
	if err != nil {
		groupError(w, err)
		return
	}
	sms, smsTotal, err := storage.SearchEnvironmentSMS(environment.ID, query, 0, limit)
	if err != nil {
		groupError(w, err)
		return
	}
	webhooks, webhookTotal, err := storage.SearchEnvironmentWebhooks(environment.ID, query, 0, limit)
	if err != nil {
		groupError(w, err)
		return
	}
	type matches struct {
		Messages any `json:"messages"`
		Total    int `json:"total"`
	}
	groupJSON(w, http.StatusOK, struct {
		Email    matches `json:"email"`
		SMS      matches `json:"sms"`
		Webhooks matches `json:"webhooks"`
	}{matches{email, emailTotal}, matches{sms, smsTotal}, matches{webhooks, webhookTotal}})
}

// EnvironmentCorrelation finds related records without crossing environment boundaries.
func EnvironmentCorrelation(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	records, err := storage.ListCorrelatedRecords(r.Context(), environment.ID, mux.Vars(r)["correlation"])
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusOK, struct {
		Records []storage.CorrelatedRecord `json:"records"`
	}{records})
}

// EnvironmentCorrelationTimeline returns paginated capture previews for a test run.
func EnvironmentCorrelationTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	start, limit := 0, 20
	if raw := r.URL.Query().Get("start"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
		start = value
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
		limit = value
	}
	records, total, err := storage.ListCorrelationTimeline(r.Context(), environment.ID, mux.Vars(r)["correlation"], start, limit)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusOK, struct {
		Records []storage.CorrelationSummary `json:"records"`
		Total   int                          `json:"total"`
	}{records, total})
}

// EnvironmentCorrelationCode finds one unambiguous code in a correlated test run.
func EnvironmentCorrelationCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	channel := r.URL.Query().Get("kind")
	if channel != "messages" && channel != "sms" {
		http.Error(w, "Choose messages or sms", http.StatusBadRequest)
		return
	}
	records, err := storage.ListCorrelatedRecords(r.Context(), environment.ID, mux.Vars(r)["correlation"])
	if err != nil {
		groupError(w, err)
		return
	}
	// The index is capped at 100 records. Older matching codes could be hidden.
	if len(records) >= 100 {
		http.Error(w, "Too many correlated records to verify a unique code", http.StatusConflict)
		return
	}
	var code string
	for _, record := range records {
		if record.Kind != channel {
			continue
		}
		kind := channel
		if kind == "messages" {
			kind = "email"
		}
		candidate, err := verificationCodeForRecord(kind, record.ID)
		switch {
		case errors.Is(err, qa.ErrNoCode):
			continue
		case errors.Is(err, qa.ErrAmbiguousCode):
			http.Error(w, "Multiple verification codes found", http.StatusConflict)
			return
		case err != nil:
			http.Error(w, "A correlated capture could not be read; retry the request", http.StatusServiceUnavailable)
			return
		case code != "" && code != candidate:
			http.Error(w, "Multiple verification codes found", http.StatusConflict)
			return
		}
		code = candidate
	}
	if code == "" {
		http.Error(w, "No verification code found", http.StatusNotFound)
		return
	}
	groupJSON(w, http.StatusOK, struct {
		Code string `json:"code"`
	}{code})
}

// EnvironmentRetention reads or updates the opt-in cleanup policy.
func EnvironmentRetention(w http.ResponseWriter, r *http.Request) {
	permission := access.ReadMessages
	if r.Method == http.MethodPut {
		permission = access.ManageProject
	}
	environment, ok := environmentForRequest(w, r, permission)
	if !ok {
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			MaxAgeSeconds *int64 `json:"max_age_seconds"`
		}
		if !decodeGroupJSON(w, r, &input) {
			return
		}
		if input.MaxAgeSeconds == nil {
			groupError(w, storage.ErrInvalidRetention)
			return
		}
		if err := storage.SetEnvironmentRetention(r.Context(), environment.ID, *input.MaxAgeSeconds); err != nil {
			groupError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	seconds, err := storage.EnvironmentRetention(r.Context(), environment.ID)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusOK, struct {
		MaxAgeSeconds int64 `json:"max_age_seconds"`
	}{seconds})
}

// EnvironmentAIStatus reports whether optional AI summaries are configured.
func EnvironmentAIStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := environmentForRequest(w, r, access.ReadMessages); !ok {
		return
	}
	groupJSON(w, http.StatusOK, struct {
		Enabled bool `json:"enabled"`
	}{config.AI.Enabled()})
}

// EnvironmentSummary explicitly sends one authorized record to the configured AI service.
func EnvironmentSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	if !config.AI.Enabled() {
		http.Error(w, "AI summaries are not configured", http.StatusServiceUnavailable)
		return
	}
	var content string
	switch scopedKind(r) {
	case "email":
		message, err := storage.ReadMessage(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body := message.Text
		if body == "" && message.HTML != "" {
			body = html2text.Strip(message.HTML, false)
		}
		content = "Subject: " + message.Subject + "\n\n" + body
	case "sms":
		message, err := storage.GetSMS(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		content = message.Body
	case "webhook":
		message, err := storage.GetWebhook(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		content = message.Method + " " + message.Path + "\n\n" + message.Body
	default:
		http.NotFound(w, r)
		return
	}
	summary, err := ai.Summarize(r.Context(), config.AI, scopedKind(r), content)
	if err != nil {
		http.Error(w, "AI service unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	groupJSON(w, http.StatusOK, struct {
		Summary string `json:"summary"`
	}{summary})
}

func verificationCodeForRecord(kind, id string) (string, error) {
	var body string
	switch kind {
	case "email":
		message, err := storage.ReadMessage(id)
		if err != nil {
			return "", err
		}
		body = message.Text
		if body == "" && message.HTML != "" {
			body = html2text.Strip(message.HTML, false)
		}
		body = message.Subject + "\n" + body
	case "sms":
		message, err := storage.GetSMS(id)
		if err != nil {
			return "", err
		}
		body = message.Body
	default:
		return "", storage.ErrRecordNotFound
	}
	return qa.VerificationCode(body)
}

// EnvironmentVerificationCode returns a unique labeled code from an authorized record.
func EnvironmentVerificationCode(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	code, err := verificationCodeForRecord(scopedKind(r), id)
	if errors.Is(err, qa.ErrNoCode) {
		http.Error(w, "No verification code found", http.StatusNotFound)
		return
	}
	if errors.Is(err, qa.ErrAmbiguousCode) {
		http.Error(w, "Multiple verification codes found", http.StatusConflict)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	groupJSON(w, http.StatusOK, struct {
		Code string `json:"code"`
	}{code})
}

// EnvironmentTokens lists or issues short-lived environment-scoped service tokens.
func EnvironmentTokens(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ManageProject)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		tokens, err := storage.ListServiceTokens(r.Context(), environment.ID)
		if err != nil {
			groupError(w, err)
			return
		}
		groupJSON(w, http.StatusOK, struct {
			Tokens []storage.ServiceToken `json:"tokens"`
		}{tokens})
		return
	}
	var input struct {
		Name       string      `json:"name"`
		Role       access.Role `json:"role"`
		TTLSeconds int64       `json:"ttl_seconds"`
	}
	if !decodeGroupJSON(w, r, &input) {
		return
	}
	if input.TTLSeconds == 0 {
		input.TTLSeconds = 3600
	}
	if input.TTLSeconds < 60 || input.TTLSeconds > 86400 {
		groupError(w, storage.ErrInvalidTokenRequest)
		return
	}
	token, secret, err := storage.IssueServiceToken(r.Context(), environment.ID, input.Name, input.Role, time.Duration(input.TTLSeconds)*time.Second)
	if err != nil {
		groupError(w, err)
		return
	}
	groupJSON(w, http.StatusCreated, struct {
		Token  storage.ServiceToken `json:"token"`
		Secret string               `json:"secret"`
	}{token, secret})
}

// RevokeEnvironmentToken revokes one environment's service token.
func RevokeEnvironmentToken(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ManageProject)
	if !ok {
		return
	}
	if err := storage.RevokeServiceToken(r.Context(), environment.ID, mux.Vars(r)["token"]); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func scopedKind(r *http.Request) string {
	switch mux.Vars(r)["kind"] {
	case "messages":
		return "email"
	case "sms":
		return "sms"
	case "webhooks":
		return "webhook"
	default:
		return ""
	}
}

func scopedRecord(w http.ResponseWriter, r *http.Request) (string, bool) {
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return "", false
	}
	id := mux.Vars(r)["record"]
	// Legacy Mailpit handlers interpret this value as the instance-wide latest
	// record. Scoped routes must never permit that shortcut.
	if id == "latest" {
		http.NotFound(w, r)
		return "", false
	}
	owned, err := storage.RecordInEnvironment(r.Context(), scopedKind(r), id, environment.ID)
	if err != nil {
		groupError(w, err)
		return "", false
	}
	if !owned {
		http.NotFound(w, r)
		return "", false
	}
	return id, true
}

// EnvironmentMessages lists records only from an authorized environment.
func EnvironmentMessages(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.ReadMessages)
	if !ok {
		return
	}
	start, before, limit := getStartLimit(r)
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var items any
	var err error
	var total *int
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if len(query) > 512 {
		http.Error(w, "Search query is too long", http.StatusBadRequest)
		return
	}
	count := 0
	switch scopedKind(r) {
	case "email":
		if query != "" {
			items, count, err = storage.SearchEnvironmentMessages(environment.ID, query, r.URL.Query().Get("timezone"), start, before, limit)
		} else {
			items, err = storage.ListEnvironmentMessages(environment.ID, start, before, limit)
		}
	case "sms":
		if query != "" {
			items, count, err = storage.SearchEnvironmentSMS(environment.ID, query, start, limit)
		} else {
			items, err = storage.ListEnvironmentSMS(environment.ID, start, limit)
		}
	case "webhook":
		if query != "" {
			items, count, err = storage.SearchEnvironmentWebhooks(environment.ID, query, start, limit)
		} else {
			items, err = storage.ListEnvironmentWebhooks(environment.ID, start, limit)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		groupError(w, err)
		return
	}
	if query != "" {
		total = &count
	}
	groupJSON(w, http.StatusOK, struct {
		Messages any  `json:"messages"`
		Total    *int `json:"total,omitempty"`
	}{items, total})
}

// EnvironmentMessage returns a single authorized record without changing its read status.
func EnvironmentMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	var item any
	var err error
	switch scopedKind(r) {
	case "email":
		item, err = storage.ReadMessage(id)
	case "sms":
		item, err = storage.GetSMS(id)
	case "webhook":
		item, err = storage.GetWebhook(id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	groupJSON(w, http.StatusOK, item)
}

// DeleteEnvironmentMessage removes an authorized record from its environment.
func DeleteEnvironmentMessage(w http.ResponseWriter, r *http.Request) {
	environment, ok := environmentForRequest(w, r, access.WriteMessages)
	if !ok {
		return
	}
	if err := storage.DeleteEnvironmentRecord(r.Context(), scopedKind(r), mux.Vars(r)["record"], environment.ID); err != nil {
		groupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EnvironmentMessageRaw returns raw email from an authorized environment.
func EnvironmentMessageRaw(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	DownloadRaw(w, mux.SetURLVars(r, map[string]string{"id": id}))
}

// EnvironmentMessageHeaders returns headers for an authorized email.
func EnvironmentMessageHeaders(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	GetHeaders(w, mux.SetURLVars(r, map[string]string{"id": id}))
}

// EnvironmentHTMLCheck checks an authorized email's HTML compatibility.
func EnvironmentHTMLCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	HTMLCheck(w, mux.SetURLVars(r, map[string]string{"id": id}))
}

// EnvironmentAttachment returns an attachment from an authorized email.
func EnvironmentAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedRecord(w, r)
	if !ok {
		return
	}
	DownloadAttachment(w, mux.SetURLVars(r, map[string]string{"id": id, "partID": mux.Vars(r)["partID"]}))
}
