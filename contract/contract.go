// Package contract is the girvi/1 plugin contract as data: the written
// description, the JSON schema of plugin.json, and a sample of every event.
// The CLI, the MCP server and the test harness all read it from here.
package contract

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

//go:embed CONTRACT.md plugin.schema.json fixtures/*.json
var files embed.FS

// Text is the written contract, in Markdown.
func Text() string {
	b, _ := files.ReadFile("CONTRACT.md")
	return string(b)
}

// Schema is the JSON schema of plugin.json.
func Schema() []byte {
	b, _ := files.ReadFile("plugin.schema.json")
	return b
}

// Events lists the event types there are samples of.
func Events() []string {
	entries, _ := files.ReadDir("fixtures")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// Fixture is a sample event of a type, with every customer field present.
func Fixture(eventType string) (json.RawMessage, error) {
	b, err := files.ReadFile("fixtures/" + eventType + ".json")
	if err != nil {
		return nil, fmt.Errorf("there is no event %q; there are %s", eventType, strings.Join(Events(), ", "))
	}
	return b, nil
}

// Customer fields a plugin must ask for in plugin.json "fields".
const (
	FieldCustomerName  = "customer.name"
	FieldCustomerPhone = "customer.phone"
)

// ForPlugin is an event as a plugin with these fields is given it: customer
// details it did not ask for are removed, as Girvi does.
func ForPlugin(event json.RawMessage, fields []string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(event))
	dec.UseNumber() // keep large amounts exact
	var env map[string]any
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("the event is not valid JSON: %w", err)
	}
	if data, ok := env["data"].(map[string]any); ok {
		if customer, ok := data["customer"].(map[string]any); ok {
			if !slices.Contains(fields, FieldCustomerName) {
				delete(customer, "name")
			}
			if !slices.Contains(fields, FieldCustomerPhone) {
				delete(customer, "phone")
				delete(customer, "phone_e164")
			}
		}
	}
	return json.Marshal(env)
}
