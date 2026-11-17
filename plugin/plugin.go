// Package plugin is how a Girvi plugin is written in Go.
//
// A plugin is a WebAssembly module that Girvi runs in a sandbox. It cannot
// touch files, the network or Girvi's records except through the Host it is
// given, and only as far as its plugin.json allows. A handler is an ordinary
// function:
//
//	func handle(h plugin.Host, in plugin.Input) plugin.Outcome {
//		loan, err := in.LoanCreated()
//		if err != nil {
//			return plugin.Failed(err.Error())
//		}
//		h.Log("loan " + loan.Loan.Number)
//		return plugin.OK("")
//	}
//
// and main.go exports it (see the scaffold made by `girvi plugin new`):
//
//	//go:wasmexport on_event
//	func onEvent() { plugin.Run(handle) }
//
// Nothing in a handler depends on WebAssembly, so it is tested with plain
// `go test` and the fake host in package plugintest.
package plugin

import (
	"encoding/json"
	"fmt"
)

// Kinds of call. An event is a delivery of something that happened; a test is
// the same, with a made-up loan, sent from the plugin's Send test button; a
// wake is a call the plugin asked for with Host.WakeAt.
const (
	KindEvent = "event"
	KindTest  = "test"
	KindWake  = "wake"
)

// Input is what Girvi passes to a handler.
type Input struct {
	Kind       string `json:"kind"`
	DeliveryID string `json:"delivery_id"` // unique per plugin and event, the same on every retry
	Attempt    int    `json:"attempt"`     // 1 on the first call
	// Event is the event as JSON, for kinds event and test. Use the typed
	// methods (LoanCreated, PaymentReceived, LoanRedeemed) to read it.
	Event json.RawMessage `json:"event,omitempty"`
	// Wake is what the plugin passed to Host.WakeAt, for kind wake.
	Wake     string            `json:"wake,omitempty"`
	Firm     Firm              `json:"firm"`
	Settings map[string]string `json:"settings"` // this firm's settings, with plugin.json defaults applied
}

// Firm is the shop the call is for. A plugin is turned on per firm, and its
// settings and storage are per firm.
type Firm struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Outcome is the plugin's decision about one call. Girvi records it; for
// Retry it calls again after the delay. Girvi never decides this for the
// plugin, except that a call that crashes or runs out of time is treated as
// having no answer (see the contract).
type Outcome struct {
	Kind           string `json:"kind"` // ok | failed | blocked | retry
	Note           string `json:"note,omitempty"`
	RetryInSeconds int    `json:"retry_in_seconds,omitempty"`
}

// OK: done. The note is shown in the plugin's activity.
func OK(note string) Outcome { return Outcome{Kind: "ok", Note: note} }

// Failed: this item will never work. The user can send it again by hand.
func Failed(note string) Outcome { return Outcome{Kind: "failed", Note: note} }

// Blocked: the shop has to fix a setting first. Girvi calls again after the
// plugin's settings change.
func Blocked(note string) Outcome { return Outcome{Kind: "blocked", Note: note} }

// Retry: try again later, after at least a second.
func Retry(seconds int, note string) Outcome {
	return Outcome{Kind: "retry", Note: note, RetryInSeconds: seconds}
}

// Request is an outbound HTTP request. The host must be allowed by
// http_hosts in plugin.json. Redirects are not followed.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// Response is what came back. Error is set when the request was not made or
// got no answer (not allowed, could not connect, timed out); then Status is 0.
// Bodies over 1 MB are cut.
type Response struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Host is everything a plugin can do outside its own memory.
type Host interface {
	// HTTP makes a request to a host plugin.json allows.
	HTTP(Request) Response
	// Log adds a line to this call's log, shown in the plugin's activity.
	Log(string)
	// StorageGet reads the plugin's own storage for this firm. It reports false
	// when the key has no value.
	StorageGet(key string) ([]byte, bool)
	// StorageSet writes it. It reports false if the value was refused (over
	// the limits in the contract).
	StorageSet(key string, value []byte) bool
	StorageDelete(key string) bool
	// WakeAt asks Girvi to call on_wake at a time (Unix seconds), with a
	// payload. It reports false if refused.
	WakeAt(unixSeconds int64, payload string) bool
}

// Handler handles one call.
type Handler func(Host, Input) Outcome

// Envelope is the part of every event that is not its data.
type Envelope struct {
	ID         string `json:"id"`   // the delivery id
	Type       string `json:"type"` // loan.created | payment.received | loan.redeemed
	Version    int    `json:"version"`
	OccurredAt string `json:"occurred_at"` // RFC 3339, UTC
}

// EventType is the type of the event, or "" for a wake.
func (in Input) EventType() string {
	var e Envelope
	_ = json.Unmarshal(in.Event, &e)
	return e.Type
}

// Envelope reads the event's envelope.
func (in Input) Envelope() (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(in.Event, &e); err != nil {
		return e, fmt.Errorf("read the event: %w", err)
	}
	return e, nil
}

func decode[T any](in Input, eventType string) (T, error) {
	var out struct {
		Envelope
		Data T `json:"data"`
	}
	if err := json.Unmarshal(in.Event, &out); err != nil {
		return out.Data, fmt.Errorf("read the event: %w", err)
	}
	if out.Type != eventType {
		return out.Data, fmt.Errorf("this is a %q event, not %q", out.Type, eventType)
	}
	return out.Data, nil
}

// LoanCreated reads a loan.created event.
func (in Input) LoanCreated() (LoanCreated, error) { return decode[LoanCreated](in, EventLoanCreated) }

// PaymentReceived reads a payment.received event.
func (in Input) PaymentReceived() (PaymentReceived, error) {
	return decode[PaymentReceived](in, EventPaymentReceived)
}

// LoanRedeemed reads a loan.redeemed event.
func (in Input) LoanRedeemed() (LoanRedeemed, error) {
	return decode[LoanRedeemed](in, EventLoanRedeemed)
}
