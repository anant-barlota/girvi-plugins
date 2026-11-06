package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// managedTemplate is a message Girvi composes, with the template it can create
// for the shop. The body is plain, in the utility category, and each variable has
// text around it, which is what Meta requires for approval.
type managedTemplate struct {
	key           string
	title         string
	defaultName   string
	defaultParams string
	body          string   // {{1}}, {{2}}... in the order of defaultParams
	examples      []string // sample values, which Meta asks for with every variable
}

var managedTemplates = []managedTemplate{
	{
		key:           "pledge_disbursed",
		title:         "Loan created",
		defaultName:   "girvi_pledge_disbursed",
		defaultParams: "customer_name,loan_number,amount,weight,interest_rate,pledged_on",
		body:          "Hello {{1}}, your loan {{2}} for {{3}} against {{4}} at {{5}} was recorded on {{6}}.",
		examples:      []string{"Ravi Kumar", "F-101", "₹15,000.00", "25.000 g", "14% p.a.", "12 Oct 2026"},
	},
	{
		key:           "payment_received",
		title:         "Payment received",
		defaultName:   "girvi_payment_received",
		defaultParams: "customer_name,loan_number,amount_paid,balance_due",
		body:          "Hello {{1}}, we received your payment of {{3}} for loan {{2}}. Balance remaining: {{4}}.",
		examples:      []string{"Ravi Kumar", "F-101", "₹5,000.00", "₹10,000.00"},
	},
	{
		key:           "loan_closed",
		title:         "Loan closed",
		defaultName:   "girvi_loan_closed",
		defaultParams: "customer_name,loan_number,weight",
		body:          "Hello {{1}}, your loan {{2}} is now closed and {{3}} of articles have been returned to you. Thank you.",
		examples:      []string{"Ravi Kumar", "F-101", "25.000 g"},
	},
}

// templateSetup is what we last learned about a firm's templates from Meta.
type templateSetup struct {
	at       time.Time
	running  bool
	statuses map[string]string // template key -> PENDING, APPROVED, REJECTED..., or "custom"
	problem  string            // why setup could not finish, in words for the shop
}

// setupEligible reports whether Girvi should manage this firm's templates, and
// returns the settings it needs.
func (w *whatsApp) setupEligible(firmID string) (values map[string]string, waba, token string, ok bool) {
	values, _, enabled := w.firm(firmID)
	waba = strings.TrimSpace(values["waba_id"])
	token = strings.TrimSpace(values["access_token"])
	if !enabled || waba == "" || token == "" || strings.TrimSpace(values["phone_number_id"]) == "" {
		return nil, "", "", false
	}
	if strings.EqualFold(strings.TrimSpace(values["create_templates"]), "false") {
		return nil, "", "", false
	}
	return values, waba, token, true
}

// refreshTemplatesAsync checks, and where needed creates, a firm's templates in
// the background. Only one run per firm happens at a time.
func (w *whatsApp) refreshTemplatesAsync(firmID string) {
	if _, _, _, ok := w.setupEligible(firmID); !ok {
		return
	}
	w.mu.Lock()
	st := w.setup[firmID]
	if st == nil {
		st = &templateSetup{statuses: map[string]string{}}
		w.setup[firmID] = st
	}
	if st.running {
		w.mu.Unlock()
		return
	}
	st.running = true
	w.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		statuses, problem := w.ensureTemplates(ctx, firmID)

		w.mu.Lock()
		defer w.mu.Unlock()
		if cur := w.setup[firmID]; cur == st { // not replaced by a newer Configure
			st.statuses, st.problem, st.at, st.running = statuses, problem, time.Now(), false
		}
	}()
}

// ensureTemplates makes sure each default-named template exists in the account.
// A template the shop named itself is left alone.
func (w *whatsApp) ensureTemplates(ctx context.Context, firmID string) (map[string]string, string) {
	values, waba, token, ok := w.setupEligible(firmID)
	if !ok {
		return nil, ""
	}
	statuses := map[string]string{}
	var problems []string
	for _, t := range managedTemplates {
		name := valueOr(values[t.key+"_template"], t.defaultName)
		lang := valueOr(values[t.key+"_language"], "en")
		if name != t.defaultName || !strings.HasPrefix(strings.ToLower(lang), "en") {
			statuses[t.key] = "custom" // the shop's own template or language: not ours to create
			continue
		}

		status, found, err := w.templateStatus(ctx, values, waba, token, name, lang)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", t.title, err))
			continue
		}
		if !found {
			if err := w.createTemplate(ctx, values, waba, token, t, name, lang); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", t.title, err))
				continue
			}
			status = "PENDING"
		}
		statuses[t.key] = status
	}
	return statuses, strings.Join(problems, "; ")
}

func (w *whatsApp) templatesEndpoint(values map[string]string, waba string) string {
	base := strings.TrimRight(valueOr(values["api_base_url"], defaultBaseURL), "/")
	version := valueOr(values["graph_version"], defaultGraphVersion)
	return fmt.Sprintf("%s/%s/%s/message_templates", base, version, url.PathEscape(waba))
}

// templateStatus looks a template up by name and language.
func (w *whatsApp) templateStatus(ctx context.Context, values map[string]string, waba, token, name, lang string) (status string, found bool, err error) {
	q := url.Values{"name": {name}, "fields": {"name,status,language"}, "limit": {"50"}}
	body, code, err := w.graph(ctx, http.MethodGet, w.templatesEndpoint(values, waba)+"?"+q.Encode(), token, nil)
	if err != nil {
		return "", false, err
	}
	if code != http.StatusOK {
		return "", false, metaProblem(code, body)
	}
	var list struct {
		Data []struct {
			Name     string `json:"name"`
			Status   string `json:"status"`
			Language string `json:"language"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", false, fmt.Errorf("Meta's answer could not be read")
	}
	for _, t := range list.Data {
		if t.Name == name && strings.EqualFold(t.Language, lang) {
			return strings.ToUpper(t.Status), true, nil
		}
	}
	return "", false, nil
}

func (w *whatsApp) createTemplate(ctx context.Context, values map[string]string, waba, token string, t managedTemplate, name, lang string) error {
	payload, _ := json.Marshal(map[string]any{
		"name":     name,
		"language": lang,
		"category": "UTILITY",
		"components": []map[string]any{{
			"type":    "BODY",
			"text":    t.body,
			"example": map[string]any{"body_text": [][]string{t.examples}},
		}},
	})
	body, code, err := w.graph(ctx, http.MethodPost, w.templatesEndpoint(values, waba), token, payload)
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return metaProblem(code, body)
	}
	return nil
}

// graph makes one call to the Graph API and returns the body and status.
func (w *whatsApp) graph(ctx context.Context, method, endpoint, token string, payload []byte) ([]byte, int, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not reach Meta: %w", redact(err, token))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, nil
}

// metaProblem turns an error answer into a sentence for the shop.
func metaProblem(status int, body []byte) error {
	var e apiError
	_ = json.Unmarshal(body, &e)
	msg := strings.TrimSpace(e.Error.Message)
	if d := strings.TrimSpace(e.Error.Data.Details); d != "" && d != msg {
		msg += " (" + d + ")"
	}
	if msg == "" {
		msg = fmt.Sprintf("Meta answered with status %d", status)
	}
	switch {
	case status == http.StatusUnauthorized || e.Error.Code == 190:
		return fmt.Errorf("Meta rejected the access token")
	case e.Error.Code == 100 || status == http.StatusNotFound:
		return fmt.Errorf("check the Messaging account ID (%s)", msg)
	}
	return fmt.Errorf("%s", msg)
}

// templateSummary says in a sentence how the firm's templates stand. bad is true
// when the shop has to do something.
func (w *whatsApp) templateSummary(firmID string) (summary string, bad bool) {
	w.mu.RLock()
	st := w.setup[firmID]
	w.mu.RUnlock()
	if _, _, _, ok := w.setupEligible(firmID); !ok {
		return "", false
	}
	if st == nil || (st.at.IsZero() && st.problem == "") {
		w.refreshTemplatesAsync(firmID)
		return "Setting up your message templates", false
	}
	// Check again now and then; the answer comes with the next health check.
	if time.Since(st.at) > healthTTL {
		w.refreshTemplatesAsync(firmID)
	}
	if st.problem != "" {
		return "Message templates need attention: " + st.problem, true
	}

	counts := map[string]int{}
	var rejected []string
	for _, t := range managedTemplates {
		status := st.statuses[t.key]
		switch status {
		case "APPROVED", "custom":
			counts["ready"]++
		case "REJECTED", "DISABLED", "PAUSED":
			rejected = append(rejected, t.title)
			counts["bad"]++
		default:
			counts["waiting"]++
		}
	}
	switch {
	case len(rejected) > 0:
		sort.Strings(rejected)
		return "Meta turned down or paused: " + strings.Join(rejected, ", ") + ". Messages for it wait until you fix it in Meta", true
	case counts["waiting"] > 0:
		return fmt.Sprintf("%d of %d message templates are waiting for Meta's approval. Messages wait until then", counts["waiting"], len(managedTemplates)), false
	}
	return "All message templates are approved", false
}
