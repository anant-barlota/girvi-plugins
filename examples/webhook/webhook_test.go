package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-plugins/sdk"
	"github.com/anant-barlota/girvi-plugins/sdk/sdktest"
)

func TestPostsSignedEvents(t *testing.T) {
	h := sdktest.New()
	out := handle(h, sdktest.Event(t, sdk.EventLoanCreated, map[string]string{"url": "https://hooks.example.com/in", "signing_secret": "s3cret"}))
	if out.Kind != "ok" {
		t.Fatalf("outcome %+v", out)
	}
	r := h.Requests[0]
	if r.Method != "POST" || r.URL != "https://hooks.example.com/in" || r.Headers["X-Girvi-Event"] != sdk.EventLoanCreated {
		t.Fatalf("request %+v", r)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(r.Headers["X-Girvi-Timestamp"] + "."))
	mac.Write(r.Body)
	if want := "v1=" + hex.EncodeToString(mac.Sum(nil)); r.Headers["X-Girvi-Signature"] != want {
		t.Errorf("signature %q, want %q", r.Headers["X-Girvi-Signature"], want)
	}
}

// The plugin decides what each answer means.
func TestOutcomes(t *testing.T) {
	for status, want := range map[int]string{204: "ok", 302: "blocked", 401: "blocked", 404: "blocked", 422: "failed", 429: "retry", 503: "retry"} {
		h := sdktest.New()
		h.Respond(sdk.Response{Status: status, Body: []byte("token Bearer abc12345")})
		out := handle(h, sdktest.Event(t, sdk.EventPaymentReceived, map[string]string{"url": "https://hooks.example.com", "auth_header": "Bearer abc12345"}))
		if out.Kind != want {
			t.Errorf("HTTP %d: %+v, want %s", status, out, want)
		}
		if strings.Contains(out.Note, "abc12345") {
			t.Errorf("HTTP %d: the note repeats a secret: %s", status, out.Note)
		}
	}
	h := sdktest.New()
	h.Respond(sdk.Response{Error: "connection refused"})
	if out := handle(h, sdktest.Event(t, sdk.EventLoanRedeemed, map[string]string{"url": "https://hooks.example.com"})); out.Kind != "retry" {
		t.Errorf("no answer from the receiver: %+v", out)
	}
}

func TestSettingsProblemsBlock(t *testing.T) {
	for name, settings := range map[string]map[string]string{
		"no URL":       {},
		"not http":     {"url": "ftp://example.com"},
		"plain public": {"url": "http://example.com"},
	} {
		if out := handle(sdktest.New(), sdktest.Event(t, sdk.EventLoanCreated, settings)); out.Kind != "blocked" {
			t.Errorf("%s: %+v", name, out)
		}
	}
}

func TestCustomerDetailsCanBeLeftOut(t *testing.T) {
	h := sdktest.New()
	handle(h, sdktest.Event(t, sdk.EventLoanCreated, map[string]string{"url": "https://hooks.example.com", "include_customer_details": "false"}))
	body := string(h.Requests[0].Body)
	if strings.Contains(body, "Asha") || strings.Contains(body, "43210") {
		t.Errorf("customer details were sent: %s", body)
	}
}
