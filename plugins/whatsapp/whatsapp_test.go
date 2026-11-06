package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-plugins/sdk"
	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
	notifyv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/notify/v1"
)

// fakeGraph stands in for graph.facebook.com and records what it was sent.
type fakeGraph struct {
	srv      *httptest.Server
	status   int
	response string
	got      struct {
		method, path, auth, contentType string
		body                            map[string]any
	}
}

func newFakeGraph(t *testing.T) *fakeGraph {
	f := &fakeGraph{status: 200, response: `{"messaging_product":"whatsapp","messages":[{"id":"wamid.ABC"}]}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.got.method, f.got.path = r.Method, r.URL.Path
		f.got.auth, f.got.contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		f.got.body = map[string]any{}
		_ = json.Unmarshal(raw, &f.got.body)
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.response))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func configured(t *testing.T, f *fakeGraph, firmValues map[string]string) *whatsApp {
	t.Helper()
	w := newWhatsApp()
	values := map[string]string{
		"phone_number_id":           "1055",
		"access_token":              "TOKEN-123",
		"pledge_disbursed_template": "pledge_created_v1",
	}
	for k, v := range firmValues {
		values[k] = v
	}
	_ = w.Configure(context.Background(), sdk.Config{
		Instance: map[string]string{"api_base_url": f.srv.URL, "graph_version": "v21.0"},
		Firms:    map[string]sdk.FirmConfig{"firm-a": {Name: "Shree Jewellers", Enabled: true, Values: values}},
	})
	return w
}

func pledgeItem() *deliveryv1.Item {
	return &deliveryv1.Item{
		DeliveryId: "d1", Capability: "notify", FirmId: "firm-a",
		Payload: &deliveryv1.Item_Notify{Notify: &notifyv1.Message{
			RecipientE164: "+919876543210",
			TemplateKey:   "pledge_disbursed",
			Language:      "en",
			Vars: map[string]string{
				"customer_name": "Ravi Kumar", "loan_number": "F-101", "amount": "₹15,000.00",
				"weight": "25.000 g", "interest_rate": "14% p.a.", "pledged_on": "12 Oct 2026",
			},
		}},
	}
}

func TestSendsTheTemplateWithTheVariablesInOrder(t *testing.T) {
	f := newFakeGraph(t)
	w := configured(t, f, nil)

	res, err := w.Handle(context.Background(), pledgeItem())
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if res.ProviderRef != "wamid.ABC" {
		t.Errorf("provider ref = %q", res.ProviderRef)
	}
	if f.got.method != "POST" || f.got.path != "/v21.0/1055/messages" {
		t.Errorf("request = %s %s", f.got.method, f.got.path)
	}
	if f.got.auth != "Bearer TOKEN-123" || f.got.contentType != "application/json" {
		t.Errorf("headers: %q %q", f.got.auth, f.got.contentType)
	}

	b := f.got.body
	if b["messaging_product"] != "whatsapp" || b["to"] != "919876543210" || b["type"] != "template" {
		t.Errorf("envelope wrong: %v", b)
	}
	tpl := b["template"].(map[string]any)
	if tpl["name"] != "pledge_created_v1" || tpl["language"].(map[string]any)["code"] != "en" {
		t.Errorf("template wrong: %v", tpl)
	}
	params := tpl["components"].([]any)[0].(map[string]any)["parameters"].([]any)
	var got []string
	for _, p := range params {
		got = append(got, p.(map[string]any)["text"].(string))
	}
	want := []string{"Ravi Kumar", "F-101", "₹15,000.00", "25.000 g", "14% p.a.", "12 Oct 2026"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("parameters = %v; want %v", got, want)
	}
}

func TestTheShopCanReorderTheVariables(t *testing.T) {
	f := newFakeGraph(t)
	w := configured(t, f, map[string]string{"pledge_disbursed_params": "loan_number, customer_name"})
	if _, err := w.Handle(context.Background(), pledgeItem()); err != nil {
		t.Fatal(err)
	}
	params := f.got.body["template"].(map[string]any)["components"].([]any)[0].(map[string]any)["parameters"].([]any)
	if len(params) != 2 || params[0].(map[string]any)["text"] != "F-101" || params[1].(map[string]any)["text"] != "Ravi Kumar" {
		t.Errorf("parameters = %v", params)
	}
}

func TestATestMessageUsesTheHelloWorldTemplateWithoutVariables(t *testing.T) {
	f := newFakeGraph(t)
	w := configured(t, f, nil)
	item := pledgeItem()
	item.GetNotify().TemplateKey = "test_ping"
	if _, err := w.Handle(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	tpl := f.got.body["template"].(map[string]any)
	if tpl["name"] != "hello_world" || tpl["language"].(map[string]any)["code"] != "en_US" {
		t.Errorf("test template wrong: %v", tpl)
	}
	if _, has := tpl["components"]; has {
		t.Error("hello_world has no variables, so no components should be sent")
	}
}

func TestParametersAreCleanedForWhatsApp(t *testing.T) {
	cases := map[string]string{
		"line one\nline two": "line one line two",
		"a\tb":               "a b",
		"x     y":            "x    y",
		"  padded  ":         "padded",
	}
	for in, want := range cases {
		if got := sanitizeParam(in); got != want {
			t.Errorf("sanitizeParam(%q) = %q; want %q", in, got, want)
		}
	}
}

func kindOf(err error) string {
	var perm *sdk.PermanentError
	var blocked *sdk.BlockedError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &perm):
		return "permanent"
	case errors.As(err, &blocked):
		return "blocked"
	}
	return "retry"
}

func TestErrorsAreClassifiedByWhatToDoNext(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"expired token", 401, `{"error":{"message":"Error validating access token","type":"OAuthException","code":190}}`, "blocked"},
		{"template does not exist", 404, `{"error":{"message":"Template name does not exist","code":132001}}`, "blocked"},
		{"wrong variable count", 400, `{"error":{"message":"Number of parameters does not match","code":132000}}`, "blocked"},
		{"number not on whatsapp", 400, `{"error":{"message":"Message undeliverable","code":131026}}`, "permanent"},
		{"not in the test allow list", 400, `{"error":{"message":"Recipient not in allowed list","code":131030}}`, "permanent"},
		{"rate limit by status", 429, `{}`, "retry"},
		{"rate limit by code", 400, `{"error":{"message":"Rate limit hit","code":130429}}`, "retry"},
		{"server error", 503, `<html>down</html>`, "retry"},
		{"unknown client error", 400, `{"error":{"message":"Something else","code":999999}}`, "permanent"},
		{"forbidden without a code", 403, `{}`, "blocked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGraph(t)
			f.status, f.response = c.status, c.body
			w := configured(t, f, nil)
			_, err := w.Handle(context.Background(), pledgeItem())
			if got := kindOf(err); got != c.want {
				t.Errorf("classified as %s (%v); want %s", got, err, c.want)
			}
		})
	}
}

func TestMissingSettingsBlockInsteadOfFailing(t *testing.T) {
	f := newFakeGraph(t)

	w := configured(t, f, map[string]string{"access_token": ""})
	if _, err := w.Handle(context.Background(), pledgeItem()); kindOf(err) != "blocked" {
		t.Errorf("missing token = %v; want blocked", err)
	}

	w = configured(t, f, map[string]string{"pledge_disbursed_template": ""})
	_, err := w.Handle(context.Background(), pledgeItem())
	if kindOf(err) != "blocked" || !strings.Contains(err.Error(), "template name") {
		t.Errorf("missing template name = %v; want blocked with a pointer to the setting", err)
	}
	if f.got.method != "" {
		t.Error("nothing should be sent while settings are missing")
	}
}

func TestAMissingVariableIsAPermanentFailure(t *testing.T) {
	f := newFakeGraph(t)
	w := configured(t, f, nil)
	item := pledgeItem()
	delete(item.GetNotify().Vars, "weight")
	if _, err := w.Handle(context.Background(), item); kindOf(err) != "permanent" {
		t.Errorf("= %v; want permanent", err)
	}
}

func TestTheTokenNeverAppearsInAnError(t *testing.T) {
	f := newFakeGraph(t)
	w := configured(t, f, nil)
	f.srv.Close() // nothing is listening now
	_, err := w.Handle(context.Background(), pledgeItem())
	if err == nil || strings.Contains(err.Error(), "TOKEN-123") {
		t.Errorf("error must exist and must not contain the token: %v", err)
	}
	if kindOf(err) != "retry" {
		t.Errorf("an unreachable network is retried, got %s", kindOf(err))
	}
}

func TestAFirmThatIsOffIsNeverSentTo(t *testing.T) {
	f := newFakeGraph(t)
	w := newWhatsApp()
	_ = w.Configure(context.Background(), sdk.Config{
		Instance: map[string]string{"api_base_url": f.srv.URL},
		Firms:    map[string]sdk.FirmConfig{"firm-a": {Enabled: false, Values: map[string]string{"phone_number_id": "1", "access_token": "t"}}},
	})
	if _, err := w.Handle(context.Background(), pledgeItem()); kindOf(err) != "permanent" {
		t.Errorf("= %v; want permanent", err)
	}
	if f.got.method != "" {
		t.Error("nothing may be sent for a firm the extension is off for")
	}
}

func TestHealthChecksCredentialsPerFirm(t *testing.T) {
	f := newFakeGraph(t)
	f.response = `{"verified_name":"Shree Jewellers","display_phone_number":"+91 98765 43210"}`
	w := configured(t, f, nil)

	h := w.Health(context.Background())
	if h.Status != corev1.HealthStatus_HEALTH_STATUS_OK || len(h.Firms) != 1 || !strings.Contains(h.Firms[0].Message, "Shree Jewellers") {
		t.Fatalf("healthy: %+v", h)
	}
	if f.got.method != "GET" || f.got.path != "/v21.0/1055" || f.got.auth != "Bearer TOKEN-123" {
		t.Errorf("health request = %s %s %q", f.got.method, f.got.path, f.got.auth)
	}

	f.status, f.response = 401, `{"error":{"code":190}}`
	w = configured(t, f, nil)
	h = w.Health(context.Background())
	if h.Status != corev1.HealthStatus_HEALTH_STATUS_UNHEALTHY || strings.Contains(h.Firms[0].Message, "TOKEN-123") {
		t.Errorf("rejected token: %+v", h)
	}

	w = configured(t, f, map[string]string{"access_token": ""})
	if h = w.Health(context.Background()); h.Status != corev1.HealthStatus_HEALTH_STATUS_DEGRADED {
		t.Errorf("incomplete settings should be degraded, got %v", h.Status)
	}
}
