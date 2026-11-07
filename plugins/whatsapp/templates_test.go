package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anant-barlota/girvi-plugins/sdk"
)

// fakeMeta behaves like the parts of the Graph API that template setup uses.
type fakeMeta struct {
	srv *httptest.Server

	mu       sync.Mutex
	existing map[string]string // template name -> status
	posts    []map[string]any
	getCalls int
	failPost *struct {
		status int
		body   string
	}
}

func newFakeMeta(t *testing.T) *fakeMeta {
	f := &fakeMeta{existing: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/v23.0/WABA-1/message_templates" && r.Method == http.MethodGet:
			f.getCalls++
			name := r.URL.Query().Get("name")
			type tpl struct {
				Name     string `json:"name"`
				Status   string `json:"status"`
				Language string `json:"language"`
			}
			out := struct {
				Data []tpl `json:"data"`
			}{Data: []tpl{}}
			if st, ok := f.existing[name]; ok {
				out.Data = append(out.Data, tpl{Name: name, Status: st, Language: "en"})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.URL.Path == "/v23.0/WABA-1/message_templates" && r.Method == http.MethodPost:
			if f.failPost != nil {
				w.WriteHeader(f.failPost.status)
				_, _ = w.Write([]byte(f.failPost.body))
				return
			}
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			f.posts = append(f.posts, body)
			f.existing[body["name"].(string)] = "PENDING"
			_, _ = w.Write([]byte(`{"id":"123","status":"PENDING","category":"UTILITY"}`))
		case r.URL.Path == "/v23.0/1055":
			_, _ = w.Write([]byte(`{"verified_name":"Shree Jewellers"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown path","code":100}}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMeta) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

func metaConfigured(t *testing.T, f *fakeMeta, firm map[string]string) *whatsApp {
	t.Helper()
	w := newWhatsApp()
	values := map[string]string{"phone_number_id": "1055", "waba_id": "WABA-1", "access_token": "TOKEN-123", "create_templates": "true"}
	for k, v := range firm {
		values[k] = v
	}
	// Set the config without triggering the background setup, so each test drives it.
	w.cfg = sdk.Config{
		Instance: map[string]string{"api_base_url": f.srv.URL},
		Firms:    map[string]sdk.FirmConfig{"firm-a": {Name: "Shree Jewellers", Enabled: true, Values: values}},
	}
	return w
}

func TestEveryDefaultTemplateHasTheRightNumberOfVariables(t *testing.T) {
	for _, tpl := range managedTemplates {
		vars := strings.Count(tpl.body, "{{")
		params := len(strings.Split(tpl.defaultParams, ","))
		if vars != params || vars != len(tpl.examples) {
			t.Errorf("%s: %d placeholders, %d variables, %d examples; they must all match or Meta rejects it", tpl.key, vars, params, len(tpl.examples))
		}
		last := -1
		for i := 1; i <= vars; i++ {
			at := strings.Index(tpl.body, "{{"+string(rune('0'+i))+"}}")
			if at < 0 {
				t.Errorf("%s: placeholder {{%d}} is missing", tpl.key, i)
			} else if at < last {
				t.Errorf("%s: placeholders must appear in order, but {{%d}} comes too early", tpl.key, i)
			}
			last = at
		}
		// Meta turns down a template with too many variables for its length. Keep
		// plenty of ordinary text around each one.
		if words := len(strings.Fields(tpl.body)); words < vars*4 {
			t.Errorf("%s: %d words for %d variables is too thin for Meta; want at least four per variable", tpl.key, words, vars)
		}
		if strings.HasPrefix(tpl.body, "{{") || strings.HasSuffix(strings.TrimSpace(tpl.body), "}}") {
			t.Errorf("%s: Meta does not allow a body that starts or ends with a variable", tpl.key)
		}
	}
}

func TestMissingTemplatesAreCreatedAsUtilityWithExamples(t *testing.T) {
	f := newFakeMeta(t)
	w := metaConfigured(t, f, nil)

	statuses, problem := w.ensureTemplates(context.Background(), "firm-a")
	if problem != "" {
		t.Fatalf("problem: %s", problem)
	}
	if f.postCount() != 3 {
		t.Fatalf("created %d templates; want 3", f.postCount())
	}
	for _, key := range []string{"pledge_disbursed", "payment_received", "loan_closed"} {
		if statuses[key] != "PENDING" {
			t.Errorf("%s status = %q; want PENDING", key, statuses[key])
		}
	}

	post := f.posts[0]
	if post["name"] != "girvi_pledge_disbursed" || post["language"] != "en" || post["category"] != "UTILITY" {
		t.Errorf("template header wrong: %v", post)
	}
	body := post["components"].([]any)[0].(map[string]any)
	if body["type"] != "BODY" || !strings.Contains(body["text"].(string), "{{6}}") {
		t.Errorf("body wrong: %v", body)
	}
	examples := body["example"].(map[string]any)["body_text"].([]any)[0].([]any)
	if len(examples) != 6 {
		t.Errorf("examples = %v; want one per variable", examples)
	}
}

func TestExistingTemplatesAreNotCreatedAgain(t *testing.T) {
	f := newFakeMeta(t)
	f.existing["girvi_pledge_disbursed"] = "APPROVED"
	f.existing["girvi_payment_received"] = "REJECTED"
	w := metaConfigured(t, f, nil)

	statuses, problem := w.ensureTemplates(context.Background(), "firm-a")
	if problem != "" {
		t.Fatalf("problem: %s", problem)
	}
	if f.postCount() != 1 {
		t.Errorf("created %d templates; want only the missing one", f.postCount())
	}
	if statuses["pledge_disbursed"] != "APPROVED" || statuses["payment_received"] != "REJECTED" || statuses["loan_closed"] != "PENDING" {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestTemplatesTheShopNamedItselfAreLeftAlone(t *testing.T) {
	f := newFakeMeta(t)
	w := metaConfigured(t, f, map[string]string{"pledge_disbursed_template": "my_own_template", "loan_closed_language": "hi"})

	statuses, _ := w.ensureTemplates(context.Background(), "firm-a")
	if statuses["pledge_disbursed"] != "custom" || statuses["loan_closed"] != "custom" {
		t.Errorf("a shop's own template or non-English language must not be created for them: %v", statuses)
	}
	if f.postCount() != 1 { // only payment_received
		t.Errorf("created %d templates; want 1", f.postCount())
	}
}

func TestNothingIsTouchedWhenSetupIsOffOrIncomplete(t *testing.T) {
	cases := map[string]map[string]string{
		"switched off":       {"create_templates": "false"},
		"no messaging id":    {"waba_id": ""},
		"no access token":    {"access_token": ""},
		"no phone number id": {"phone_number_id": ""},
	}
	for name, firm := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeMeta(t)
			w := metaConfigured(t, f, firm)
			statuses, problem := w.ensureTemplates(context.Background(), "firm-a")
			if statuses != nil || problem != "" || f.postCount() != 0 || f.getCalls != 0 {
				t.Errorf("must do nothing: statuses=%v problem=%q posts=%d gets=%d", statuses, problem, f.postCount(), f.getCalls)
			}
		})
	}
}

func TestAProblemFromMetaIsSaidInWords(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		want         string
		mustNotMatch string
	}{
		{"Meta's own explanation is shown", 400,
			`{"error":{"message":"Invalid parameter","code":100,"error_user_title":"Too many variables","error_user_msg":"This template has too many variables for its length."}}`,
			"Too many variables: This template has too many variables for its length.", "Messaging account ID"},
		{"an unexplained invalid parameter does not blame the account id", 400,
			`{"error":{"message":"Invalid parameter","code":100}}`,
			"Invalid parameter", "Messaging account ID"},
		{"an account that is not found points at its id", 404,
			`{"error":{"message":"Unsupported get request. Object with ID does not exist","code":100}}`,
			"Check its ID", ""},
		{"a rejected token", 401,
			`{"error":{"message":"Error validating access token","code":190}}`,
			"rejected the access token", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeMeta(t)
			f.failPost = &struct {
				status int
				body   string
			}{c.status, c.body}
			w := metaConfigured(t, f, nil)
			_, problem := w.ensureTemplates(context.Background(), "firm-a")
			if !strings.Contains(problem, c.want) {
				t.Errorf("problem = %q; want it to contain %q", problem, c.want)
			}
			if c.mustNotMatch != "" && strings.Contains(problem, c.mustNotMatch) {
				t.Errorf("problem = %q; it must not mention %q", problem, c.mustNotMatch)
			}
			if strings.Contains(problem, "TOKEN-123") {
				t.Error("the token must never appear in a message")
			}
		})
	}
}

func waitForSetup(t *testing.T, w *whatsApp, firmID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.RLock()
		st := w.setup[firmID]
		done := st != nil && !st.running && !st.at.IsZero()
		w.mu.RUnlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("template setup did not finish")
}

func TestConfiguringTheExtensionSetsUpTemplatesInTheBackground(t *testing.T) {
	f := newFakeMeta(t)
	w := newWhatsApp()
	err := w.Configure(context.Background(), sdk.Config{
		Instance: map[string]string{"api_base_url": f.srv.URL},
		Firms: map[string]sdk.FirmConfig{"firm-a": {Name: "Shree Jewellers", Enabled: true, Values: map[string]string{
			"phone_number_id": "1055", "waba_id": "WABA-1", "access_token": "TOKEN-123", "create_templates": "true",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSetup(t, w, "firm-a")
	if f.postCount() != 3 {
		t.Errorf("created %d templates after Configure; want 3", f.postCount())
	}
}

func TestHealthSaysHowTheTemplatesStand(t *testing.T) {
	f := newFakeMeta(t)
	w := metaConfigured(t, f, nil)

	w.setup = map[string]*templateSetup{"firm-a": {at: time.Now(), statuses: map[string]string{
		"pledge_disbursed": "APPROVED", "payment_received": "PENDING", "loan_closed": "PENDING",
	}}}
	if s, bad := w.templateSummary("firm-a"); bad || !strings.Contains(s, "2 of 3 message templates are waiting") {
		t.Errorf("waiting: %q bad=%v", s, bad)
	}

	w.setup["firm-a"].statuses = map[string]string{"pledge_disbursed": "APPROVED", "payment_received": "APPROVED", "loan_closed": "APPROVED"}
	if s, bad := w.templateSummary("firm-a"); bad || s != "All message templates are approved" {
		t.Errorf("approved: %q bad=%v", s, bad)
	}

	w.setup["firm-a"].statuses["loan_closed"] = "REJECTED"
	if s, bad := w.templateSummary("firm-a"); !bad || !strings.Contains(s, "Loan closed") {
		t.Errorf("rejected must name the template and ask for action: %q bad=%v", s, bad)
	}

	w.setup["firm-a"].problem = "Loan created: check the Messaging account ID"
	if s, bad := w.templateSummary("firm-a"); !bad || !strings.Contains(s, "need attention") {
		t.Errorf("problem: %q bad=%v", s, bad)
	}

	// With setup off there is nothing to say.
	off := metaConfigured(t, f, map[string]string{"create_templates": "false"})
	if s, bad := off.templateSummary("firm-a"); s != "" || bad {
		t.Errorf("setup off: %q bad=%v", s, bad)
	}
}
