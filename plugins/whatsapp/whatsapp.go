package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anant-barlota/girvi-plugins/sdk"
	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

const (
	defaultBaseURL      = "https://graph.facebook.com"
	defaultGraphVersion = "v23.0"
	healthTTL           = 5 * time.Minute
)

type whatsApp struct {
	client *http.Client

	mu     sync.RWMutex
	cfg    sdk.Config
	health map[string]cachedHealth
	setup  map[string]*templateSetup // per firm: what Meta says about our templates
}

type cachedHealth struct {
	status  corev1.HealthStatus
	message string
	at      time.Time
}

func newWhatsApp() *whatsApp {
	return &whatsApp{client: &http.Client{Timeout: 20 * time.Second}, health: map[string]cachedHealth{}, setup: map[string]*templateSetup{}}
}

func (w *whatsApp) Info() sdk.Info {
	firm, instance := corev1.SettingScope_SETTING_SCOPE_FIRM, corev1.SettingScope_SETTING_SCOPE_INSTANCE
	settings := []*corev1.SettingField{
		{Key: "phone_number_id", Title: "Phone number ID", Description: "In your Meta app: WhatsApp, API Setup, under \"From\". It is an ID, not the phone number itself.", Type: "string", Required: true, Scope: firm},
		{Key: "waba_id", Title: "Messaging account ID", Description: "In the same place, under \"WhatsApp Business Account ID\". Needed to set up your message templates.", Type: "string", Scope: firm},
		{Key: "access_token", Title: "Access token", Description: "A permanent token from a system user in Meta Business Settings. The temporary token on the API Setup page stops working after a day.", Type: "string", Secret: true, Required: true, Scope: firm},
		{Key: "create_templates", Title: "Set up message templates for me", Description: "Girvi creates its three message templates in your account and sends them to Meta for approval. Turn it off if you manage templates yourself.", Type: "boolean", DefaultValue: "true", Scope: firm},
		{Key: "api_base_url", Title: "API address", Description: "Leave as it is unless you go through a proxy.", Type: "string", DefaultValue: defaultBaseURL, Scope: instance, Advanced: true},
		{Key: "graph_version", Title: "Graph API version", Description: "The WhatsApp Cloud API version to call.", Type: "string", DefaultValue: defaultGraphVersion, Scope: instance, Advanced: true},
	}
	for _, t := range managedTemplates {
		settings = append(settings,
			&corev1.SettingField{Key: t.key + "_template", Title: t.title + ": template name", Description: "Leave empty to use " + t.defaultName + ", which Girvi creates for you. Enter your own approved template to use it instead.", Type: "string", DefaultValue: "", Scope: firm, Advanced: true},
			&corev1.SettingField{Key: t.key + "_language", Title: t.title + ": template language", Description: "The language code the template was approved in, for example en or en_US.", Type: "string", DefaultValue: "en", Scope: firm, Advanced: true},
			&corev1.SettingField{Key: t.key + "_params", Title: t.title + ": template variables", Description: "The Girvi values that fill {{1}}, {{2}} and so on, in order, separated by commas.", Type: "string", DefaultValue: t.defaultParams, Scope: firm, Advanced: true},
		)
	}
	settings = append(settings,
		&corev1.SettingField{Key: "test_template", Title: "Test message: template name", Description: "Used by Send test. Meta gives every new number a template called hello_world.", Type: "string", DefaultValue: "hello_world", Scope: firm, Advanced: true},
		&corev1.SettingField{Key: "test_language", Title: "Test message: template language", Type: "string", DefaultValue: "en_US", Scope: firm, Advanced: true},
	)

	return sdk.Info{
		ID:          "girvi/whatsapp",
		Name:        "WhatsApp Cloud API",
		Author:      "Girvi",
		Description: "Sends loan and payment messages to customers from your own WhatsApp Business number.",
		Version:     "0.2.0",
		Capabilities: []*corev1.Capability{
			{Name: "notify", Version: 1},
		},
		Requires: []string{"network", "storage.private"},
		Settings: settings,
	}
}

func (w *whatsApp) Configure(_ context.Context, cfg sdk.Config) error {
	w.mu.Lock()
	w.cfg = cfg
	w.health = map[string]cachedHealth{} // credentials may have changed
	w.setup = map[string]*templateSetup{}
	var enabled []string
	for id, f := range cfg.Firms {
		if f.Enabled {
			enabled = append(enabled, id)
		}
	}
	w.mu.Unlock()

	// Setting up templates talks to Meta, so it happens in the background.
	for _, id := range enabled {
		w.refreshTemplatesAsync(id)
	}
	return nil
}

// firm returns the values for one firm layered over the instance values.
func (w *whatsApp) firm(firmID string) (values map[string]string, name string, enabled bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	values = map[string]string{}
	for k, v := range w.cfg.Instance {
		values[k] = v
	}
	f, ok := w.cfg.Firms[firmID]
	if !ok {
		return values, "", false
	}
	for k, v := range f.Values {
		values[k] = v
	}
	return values, f.Name, f.Enabled
}

func (w *whatsApp) endpoint(values map[string]string, phoneNumberID, path string) string {
	base := strings.TrimRight(valueOr(values["api_base_url"], defaultBaseURL), "/")
	version := valueOr(values["graph_version"], defaultGraphVersion)
	return fmt.Sprintf("%s/%s/%s%s", base, version, url.PathEscape(phoneNumberID), path)
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// Handle sends one message. Anything that needs the shop to fix a setting is
// blocked, so the message waits and goes out once the setting is right.
func (w *whatsApp) Handle(ctx context.Context, item *deliveryv1.Item) (sdk.Result, error) {
	msg := item.GetNotify()
	if msg == nil {
		return sdk.Result{}, sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_UNSUPPORTED, "only messages are sent")
	}
	values, _, enabled := w.firm(item.GetFirmId())
	if !enabled {
		return sdk.Result{}, sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "this extension is not turned on for the firm")
	}

	phoneNumberID := strings.TrimSpace(values["phone_number_id"])
	token := strings.TrimSpace(values["access_token"])
	if phoneNumberID == "" || token == "" {
		return sdk.Result{}, sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "enter the phone number ID and access token in the WhatsApp settings")
	}

	tpl, err := w.template(values, msg.GetTemplateKey())
	if err != nil {
		return sdk.Result{}, err
	}
	params := make([]map[string]string, 0, len(tpl.params))
	for _, name := range tpl.params {
		v, ok := msg.GetVars()[name]
		if !ok {
			return sdk.Result{}, sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT, "the message has no value for the variable %q", name)
		}
		params = append(params, map[string]string{"type": "text", "text": sanitizeParam(v)})
	}

	body := map[string]any{
		"messaging_product": "whatsapp",
		"to":                strings.TrimPrefix(msg.GetRecipientE164(), "+"),
		"type":              "template",
		"template": map[string]any{
			"name":     tpl.name,
			"language": map[string]string{"code": tpl.language},
		},
	}
	if len(params) > 0 {
		body["template"].(map[string]any)["components"] = []map[string]any{{"type": "body", "parameters": params}}
	}
	return w.send(ctx, w.endpoint(values, phoneNumberID, "/messages"), token, body)
}

type template struct {
	name     string
	language string
	params   []string
}

// template looks up how a host message key maps onto the shop's approved template.
func (w *whatsApp) template(values map[string]string, key string) (template, error) {
	if key == "test_ping" {
		return template{
			name:     valueOr(values["test_template"], "hello_world"),
			language: valueOr(values["test_language"], "en_US"),
		}, nil
	}
	for _, t := range managedTemplates {
		if t.key != key {
			continue
		}
		name := valueOr(values[t.key+"_template"], t.defaultName)
		var params []string
		for _, p := range strings.Split(valueOr(values[t.key+"_params"], t.defaultParams), ",") {
			if p = strings.TrimSpace(p); p != "" {
				params = append(params, p)
			}
		}
		return template{name: name, language: valueOr(values[t.key+"_language"], "en"), params: params}, nil
	}
	return template{}, sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_UNSUPPORTED, "this extension does not send %q messages", key)
}

// sanitizeParam meets WhatsApp's rules for template parameters: no new lines or
// tabs, and no run of more than four spaces.
func sanitizeParam(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
	for strings.Contains(s, "     ") {
		s = strings.ReplaceAll(s, "     ", "    ")
	}
	return strings.TrimSpace(s)
}

func (w *whatsApp) send(ctx context.Context, endpoint, token string, body map[string]any) (sdk.Result, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return sdk.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return sdk.Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return sdk.Result{}, fmt.Errorf("could not reach WhatsApp: %w", redact(err, token))
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var ok struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(payload, &ok); err != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
			return sdk.Result{}, fmt.Errorf("WhatsApp accepted the request but returned no message id")
		}
		return sdk.Result{ProviderRef: ok.Messages[0].ID}, nil
	}
	return sdk.Result{}, classify(resp.StatusCode, payload)
}

func redact(err error, token string) error {
	if token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "***"))
}

type apiError struct {
	Error struct {
		Message   string `json:"message"`
		Type      string `json:"type"`
		Code      int    `json:"code"`
		Subcode   int    `json:"error_subcode"`
		FbtraceID string `json:"fbtrace_id"`
		Data      struct {
			Details string `json:"details"`
		} `json:"error_data"`
	} `json:"error"`
}

// classify decides what a failed call means. The meaning of each code follows
// Meta's WhatsApp Cloud API error list and should be checked against the current
// documentation when this extension is updated.
//
//   - A problem the shop fixes in settings (token, template, number) is blocked:
//     the message waits and is sent once the setting is right.
//   - A problem with this recipient or message is permanent.
//   - Rate limits, outages and anything unknown on the server side are retried.
func classify(status int, body []byte) error {
	var e apiError
	_ = json.Unmarshal(body, &e)
	detail := strings.TrimSpace(e.Error.Message)
	if d := strings.TrimSpace(e.Error.Data.Details); d != "" && d != detail {
		detail += " (" + d + ")"
	}
	if detail == "" {
		detail = fmt.Sprintf("WhatsApp answered with status %d", status)
	}
	msg := fmt.Sprintf("%s [code %d]", detail, e.Error.Code)

	switch {
	case status == http.StatusTooManyRequests, status >= 500:
		return fmt.Errorf("WhatsApp is busy or unavailable: %s", msg)
	}

	switch e.Error.Code {
	case 4, 80007, 130429, 131048, 131056: // too many calls, rate or spam limits
		return fmt.Errorf("WhatsApp rate limit: %s", msg)
	case 1, 2, 131000, 131016: // transient server-side trouble
		return fmt.Errorf("WhatsApp is temporarily unavailable: %s", msg)

	case 0, 190, 102, 104: // token invalid, expired or missing
		return sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "WhatsApp rejected the access token: %s", msg)
	case 10, 200, 131031, 131042: // permission, account locked, payment problem
		return sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "the WhatsApp account needs attention: %s", msg)
	case 132001, 132015, 132016, 133010: // template missing, paused or disabled; number not registered
		return sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "check the template and number in the WhatsApp settings: %s", msg)
	case 132000, 132012: // wrong number or format of template variables
		return sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "the template variables do not match the template: %s", msg)

	case 131026, 131021, 131030, 131047, 131051: // undeliverable, not allowed, window closed
		return sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT, "%s", msg)
	case 132005, 132007, 131008, 131009: // content problems
		return sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT, "%s", msg)
	}

	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return sdk.Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "WhatsApp refused the request: %s", msg)
	}
	return sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_PROVIDER, "%s", msg)
}

// Health checks each firm's credentials against the API, at most every few
// minutes.
func (w *whatsApp) Health(ctx context.Context) sdk.Health {
	w.mu.RLock()
	firms := make(map[string]sdk.FirmConfig, len(w.cfg.Firms))
	for id, f := range w.cfg.Firms {
		firms[id] = f
	}
	w.mu.RUnlock()

	out := sdk.Health{Status: corev1.HealthStatus_HEALTH_STATUS_OK, Message: "Running"}
	enabled := 0
	for id, f := range firms {
		if !f.Enabled {
			continue
		}
		enabled++
		h := w.firmHealth(ctx, id)
		out.Firms = append(out.Firms, &corev1.FirmHealth{FirmId: id, Status: h.status, Message: h.message})
		if h.status > out.Status {
			out.Status, out.Message = h.status, fmt.Sprintf("%s: %s", f.Name, h.message)
		}
	}
	if enabled == 0 {
		out.Message = "Running, not turned on for any firm"
	}
	return out
}

func (w *whatsApp) firmHealth(ctx context.Context, firmID string) cachedHealth {
	w.mu.RLock()
	cached, ok := w.health[firmID]
	w.mu.RUnlock()
	if ok && time.Since(cached.at) < healthTTL {
		return cached
	}

	values, _, _ := w.firm(firmID)
	phoneNumberID := strings.TrimSpace(values["phone_number_id"])
	token := strings.TrimSpace(values["access_token"])
	h := cachedHealth{at: time.Now()}
	switch {
	case phoneNumberID == "" || token == "":
		h.status, h.message = corev1.HealthStatus_HEALTH_STATUS_DEGRADED, "Settings are incomplete: enter the phone number ID and access token."
	default:
		h.status, h.message = w.checkCredentials(ctx, values, phoneNumberID, token)
		if h.status == corev1.HealthStatus_HEALTH_STATUS_OK {
			if extra, bad := w.templateSummary(firmID); extra != "" {
				h.message += ". " + extra
				if bad {
					h.status = corev1.HealthStatus_HEALTH_STATUS_DEGRADED
				}
			}
		}
	}

	w.mu.Lock()
	w.health[firmID] = h
	w.mu.Unlock()
	return h
}

func (w *whatsApp) checkCredentials(ctx context.Context, values map[string]string, phoneNumberID, token string) (corev1.HealthStatus, string) {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, w.endpoint(values, phoneNumberID, "")+"?fields=verified_name,display_phone_number", nil)
	if err != nil {
		return corev1.HealthStatus_HEALTH_STATUS_DEGRADED, "Could not check the connection."
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := w.client.Do(req)
	if err != nil {
		return corev1.HealthStatus_HEALTH_STATUS_DEGRADED, "Could not reach WhatsApp to check the settings."
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		var info struct {
			VerifiedName string `json:"verified_name"`
			Display      string `json:"display_phone_number"`
		}
		_ = json.Unmarshal(payload, &info)
		if info.VerifiedName != "" {
			return corev1.HealthStatus_HEALTH_STATUS_OK, fmt.Sprintf("Connected as %s", info.VerifiedName)
		}
		return corev1.HealthStatus_HEALTH_STATUS_OK, "Connected"
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return corev1.HealthStatus_HEALTH_STATUS_UNHEALTHY, "WhatsApp rejected the access token."
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusBadRequest:
		return corev1.HealthStatus_HEALTH_STATUS_UNHEALTHY, "WhatsApp does not recognize the phone number ID."
	default:
		return corev1.HealthStatus_HEALTH_STATUS_DEGRADED, fmt.Sprintf("WhatsApp answered with status %d.", resp.StatusCode)
	}
}
