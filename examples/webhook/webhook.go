// Command webhook is a Girvi plugin that posts loan events as signed JSON to a
// URL each firm chooses. Nothing in this file knows about WebAssembly: handle
// is an ordinary function, tested natively in webhook_test.go.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/anant-barlota/girvi-plugins/sdk"
)

const version = "0.2.0"

var now = time.Now

func isTrue(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "true") }

// target checks the URL a firm configured. A problem the shop can fix is
// reported as blocked, so the event waits and is sent once the URL is right.
func target(values map[string]string) (*url.URL, string) {
	raw := strings.TrimSpace(values["url"])
	if raw == "" {
		return nil, "enter the webhook URL in the settings"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "the webhook URL must start with https:// or http://"
	}
	if u.Scheme == "http" && !onOwnNetwork(u.Hostname()) && !isTrue(values["allow_plain_http"]) {
		return nil, "the webhook URL uses plain http, so customer details would travel unencrypted. Use https, or turn on \"Allow plain http\" under Advanced"
	}
	return u, ""
}

func onOwnNetwork(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.IsLoopback() || ip.IsPrivate())
}

func handle(h sdk.Host, in sdk.Input) sdk.Outcome {
	u, problem := target(in.Settings)
	if problem != "" {
		return sdk.Blocked(problem)
	}

	body, id, typ, err := build(in, !strings.EqualFold(strings.TrimSpace(in.Settings["include_customer_details"]), "false"))
	if err != nil {
		return sdk.Failed(err.Error())
	}

	timestamp := strconv.FormatInt(now().Unix(), 10)
	headers := map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        "Girvi-Webhook/" + version,
		"X-Girvi-Delivery":  id,
		"X-Girvi-Event":     typ,
		"X-Girvi-Timestamp": timestamp,
	}
	if secret := in.Settings["signing_secret"]; secret != "" {
		headers["X-Girvi-Signature"] = sign(secret, timestamp, body)
	}
	if auth := strings.TrimSpace(in.Settings["auth_header"]); auth != "" {
		headers["Authorization"] = auth
	}

	resp := h.HTTP(sdk.Request{Method: "POST", URL: u.String(), Headers: headers, Body: body})
	if resp.Error != "" {
		return sdk.Retry(300, fmt.Sprintf("could not reach %s://%s%s: %s", u.Scheme, u.Host, u.Path, resp.Error))
	}
	snippet := resp.Body
	if len(snippet) > 512 {
		snippet = snippet[:512]
	}
	return classify(resp.Status, u, scrub(snippet, in.Settings["signing_secret"], in.Settings["auth_header"]))
}

// build adds the firm to the event and leaves out the customer's details unless
// the shop chose to share them. Numbers are kept exactly as the host sent them.
func build(in sdk.Input, includeDetails bool) (body []byte, id, typ string, err error) {
	dec := json.NewDecoder(bytes.NewReader(in.Event))
	dec.UseNumber()
	var env map[string]any
	if err := dec.Decode(&env); err != nil {
		return nil, "", "", fmt.Errorf("could not read the event: %v", err)
	}
	id, _ = env["id"].(string)
	typ, _ = env["type"].(string)
	if id == "" || typ == "" {
		return nil, "", "", fmt.Errorf("the event has no id or type")
	}
	env["firm"] = in.Firm
	if !includeDetails {
		if data, ok := env["data"].(map[string]any); ok {
			if c, ok := data["customer"].(map[string]any); ok {
				data["customer"] = map[string]any{"id": c["id"]}
			}
		}
	}
	body, err = json.Marshal(env)
	return body, id, typ, err
}

// scrub hides anything this plugin was given as a secret from text that came
// back from the receiver.
func scrub(text []byte, secrets ...string) []byte {
	out := string(text)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		parts := []string{secret}
		if _, credential, found := strings.Cut(secret, " "); found {
			parts = append(parts, strings.TrimSpace(credential))
		}
		for _, p := range parts {
			if len(p) >= 4 {
				out = strings.ReplaceAll(out, p, "[hidden]")
			}
		}
	}
	return []byte(out)
}

// sign is HMAC-SHA256, keyed with the secret, over the timestamp, a dot and the
// exact body.
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// classify decides what the receiver's answer means. This is the plugin's own
// policy; the host only records the outcome.
func classify(status int, u *url.URL, body []byte) sdk.Outcome {
	switch {
	case status >= 200 && status < 300:
		return sdk.OK(fmt.Sprintf("HTTP %d from %s", status, u.Host))
	case status >= 300 && status < 400:
		return sdk.Blocked(fmt.Sprintf("the webhook URL redirects (HTTP %d). Enter the address it redirects to", status))
	case status == 401, status == 403:
		return sdk.Blocked(fmt.Sprintf("the receiver refused the request (HTTP %d). Check the signing secret and the authorization header%s", status, reason(body)))
	case status == 404, status == 410:
		return sdk.Blocked(fmt.Sprintf("the receiver says that address does not exist (HTTP %d). Check the webhook URL", status))
	case status == 408, status == 425, status == 429, status >= 500:
		return sdk.Retry(300, fmt.Sprintf("the receiver is busy or unavailable (HTTP %d)", status))
	default:
		return sdk.Failed(fmt.Sprintf("the receiver refused this event (HTTP %d)%s", status, reason(body)))
	}
}

func reason(body []byte) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, string(body))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	if s == "" {
		return ""
	}
	return ": " + s
}
