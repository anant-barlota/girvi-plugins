// Package plugintest is a fake Girvi host for testing a plugin's handlers with
// plain `go test`, no WebAssembly involved.
//
//	h := plugintest.New()
//	h.Respond(plugin.Response{Status: 503})
//	out := handle(h, plugintest.Event(t, plugin.EventLoanCreated, map[string]string{"url": "https://example.com"}))
//	if out.Kind != "retry" { ... }
//	_ = h.Requests[0] // what the plugin sent
//
// It does not check plugin.json (allowed hosts, limits). `girvi plugin test`
// runs the built plugin in a sandbox that does.
package plugintest

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/anant-barlota/girvi-sdk/plugin"
	"github.com/anant-barlota/girvi-sdk/plugin/contract"
)

// Wake is a wake-up the plugin asked for.
type Wake struct {
	At      int64
	Payload string
}

// Host records what a plugin does and answers its HTTP requests from a script.
type Host struct {
	Requests []plugin.Request
	Logs     []string
	Storage  map[string][]byte
	Wakes    []Wake

	responses []plugin.Response
}

var _ plugin.Host = (*Host)(nil)

func New() *Host { return &Host{Storage: map[string][]byte{}} }

// Respond queues answers for the next HTTP requests, in order. When none are
// queued, a request gets status 200 with an empty body.
func (h *Host) Respond(responses ...plugin.Response) { h.responses = append(h.responses, responses...) }

func (h *Host) HTTP(r plugin.Request) plugin.Response {
	h.Requests = append(h.Requests, r)
	if len(h.responses) == 0 {
		return plugin.Response{Status: 200}
	}
	resp := h.responses[0]
	h.responses = h.responses[1:]
	return resp
}

func (h *Host) Log(line string) { h.Logs = append(h.Logs, line) }

func (h *Host) StorageGet(key string) ([]byte, bool) {
	v, ok := h.Storage[key]
	return v, ok
}

func (h *Host) StorageSet(key string, value []byte) bool {
	h.Storage[key] = append([]byte(nil), value...)
	return true
}

func (h *Host) StorageDelete(key string) bool {
	delete(h.Storage, key)
	return true
}

func (h *Host) WakeAt(unixSeconds int64, payload string) bool {
	h.Wakes = append(h.Wakes, Wake{At: unixSeconds, Payload: payload})
	return true
}

// Keys lists what is in storage, sorted.
func (h *Host) Keys() []string {
	keys := make([]string, 0, len(h.Storage))
	for k := range h.Storage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Event is an input carrying the contract's sample event of a type, with all
// customer fields present, for the firm "bk-moolchand".
func Event(t testing.TB, eventType string, settings map[string]string) plugin.Input {
	t.Helper()
	raw, err := contract.Fixture(eventType)
	if err != nil {
		t.Fatal(err)
	}
	return Input(t, plugin.KindEvent, raw, settings)
}

// WakeInput is the input of a scheduled call: on_wake with the payload the plugin
// passed to WakeAt, for the firm "bk-moolchand".
func WakeInput(payload string, settings map[string]string) plugin.Input {
	if settings == nil {
		settings = map[string]string{}
	}
	return plugin.Input{
		Kind: plugin.KindWake, DeliveryID: "01a1f3c0-0000-7000-8000-000000000001", Attempt: 1, Wake: payload,
		Firm: plugin.Firm{ID: "bk-moolchand", Name: "BK Moolchand & Co."}, Settings: settings,
	}
}

// Input builds an input around any event JSON.
func Input(t testing.TB, kind string, event json.RawMessage, settings map[string]string) plugin.Input {
	t.Helper()
	var e plugin.Envelope
	if err := json.Unmarshal(event, &e); err != nil {
		t.Fatal(fmt.Errorf("the event is not valid JSON: %w", err))
	}
	if settings == nil {
		settings = map[string]string{}
	}
	return plugin.Input{
		Kind: kind, DeliveryID: e.ID, Attempt: 1, Event: event,
		Firm: plugin.Firm{ID: "bk-moolchand", Name: "BK Moolchand & Co."}, Settings: settings,
	}
}
