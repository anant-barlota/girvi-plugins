// Package harness runs a built plugin the way Girvi does, without Girvi. It
// uses Girvi's own runtime (package sandbox): the same import checks, host
// functions, limits and rules for which hosts a request may reach. Only what
// lies outside the plugin differs: storage is in memory, and network requests
// are answered from a script and recorded, so a test is repeatable and never
// sends anything anywhere. Girvi and a harness built from the same version of
// this module run a plugin the same way.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/manifest"
	"github.com/anant-barlota/girvi-plugins/sandbox"
	"github.com/anant-barlota/girvi-plugins/sdk"
)

// Plugin is a loaded plugin, ready to call.
type Plugin struct {
	Manifest manifest.Manifest
	engine   *sandbox.Engine
	module   *sandbox.Module
	mu       sync.Mutex // one call at a time, as Girvi calls a plugin per firm
}

// Load checks a plugin's code against its manifest and compiles it, exactly
// as Girvi does at install. Nothing in the plugin runs.
func Load(ctx context.Context, manifestJSON, wasm []byte) (*Plugin, error) {
	m, err := manifest.Parse(manifestJSON)
	if err != nil {
		return nil, err
	}
	e, err := sandbox.NewEngine(ctx, nil)
	if err != nil {
		return nil, err
	}
	mod, err := e.Compile(ctx, m, wasm)
	if err != nil {
		e.Close(ctx)
		return nil, err
	}
	return &Plugin{Manifest: m, engine: e, module: mod}, nil
}

// LoadBundle loads a bundle zip.
func LoadBundle(ctx context.Context, zipBytes []byte) (*Plugin, error) {
	b, err := manifest.ReadBundle(zipBytes)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b.Manifest)
	if err != nil {
		return nil, err
	}
	return Load(ctx, raw, b.Wasm)
}

// Exports lists the functions the plugin exports for Girvi.
func (p *Plugin) Exports() []string { return p.module.Exports() }

func (p *Plugin) Close(ctx context.Context) { p.engine.Close(ctx) }

// Options shape one call.
type Options struct {
	// Settings are this firm's values. Defaults from plugin.json are applied,
	// and keys it does not declare are dropped, as Girvi does.
	Settings map[string]string
	// Responses answer the plugin's allowed HTTP requests, in order. When
	// none are left, a request gets status 200 with an empty body.
	Responses []sdk.Response
	// Storage is what the plugin's storage holds before the call.
	Storage map[string][]byte
	// Deadline is how long the call may run; Girvi's is 30 seconds.
	Deadline time.Duration
}

// Request is an HTTP request the plugin made. Refused is why Girvi would not
// have made it; empty when allowed.
type Request = sandbox.Request

// Wake is a wake-up the plugin asked for.
type Wake = sandbox.Wake

// Result is what one call did.
type Result struct {
	Export  string       `json:"export"`
	Input   sdk.Input    `json:"input"`
	Outcome *sdk.Outcome `json:"outcome,omitempty"`
	// NoAnswer is why the call ended without an outcome (a crash, a timeout,
	// no outcome written). Girvi treats that as described under "No answer".
	NoAnswer string            `json:"no_answer,omitempty"`
	Logs     []string          `json:"logs,omitempty"`
	Requests []Request         `json:"requests,omitempty"`
	Storage  map[string]string `json:"storage,omitempty"` // after the call, values as text
	Wakes    []Wake            `json:"wakes,omitempty"`
	Duration time.Duration     `json:"duration_ns"`
	// Runtime is the version of this module, and so of Girvi's runtime, the
	// call ran with; "" for a build from a checkout.
	Runtime string `json:"runtime"`
}

// Settings is what a plugin is given for a firm: declared keys only, with
// defaults filled in.
func Settings(m manifest.Manifest, saved map[string]string) map[string]string {
	return m.SettingsFor(saved)
}

// Input builds the input Girvi would give the plugin for an event: the
// contract's sample of that type, with the customer details the manifest does
// not ask for removed. kind is sdk.KindEvent or sdk.KindTest.
func (p *Plugin) Input(eventType, kind string, saved map[string]string) (sdk.Input, error) {
	if !p.Manifest.Wants(eventType) && kind == sdk.KindEvent {
		return sdk.Input{}, fmt.Errorf("plugin.json does not subscribe to %q, so Girvi would never send it", eventType)
	}
	raw, err := contract.Fixture(eventType)
	if err != nil {
		return sdk.Input{}, err
	}
	event, err := contract.ForPlugin(raw, p.Manifest.Fields)
	if err != nil {
		return sdk.Input{}, err
	}
	var env sdk.Envelope
	_ = json.Unmarshal(event, &env)
	return sdk.Input{
		Kind: kind, DeliveryID: env.ID, Attempt: 1, Event: event,
		Firm: sdk.Firm{ID: "test-firm", Name: "Test Firm"}, Settings: p.Manifest.SettingsFor(saved),
	}, nil
}

// script answers allowed requests from Options.Responses, in order.
type script struct {
	mu        sync.Mutex
	responses []sdk.Response
}

func (s *script) Send(context.Context, sdk.Request, bool) sdk.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.responses) == 0 {
		return sdk.Response{Status: 200}
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp
}

// Call runs one export ("on_event" or "on_wake") with an input.
func (p *Plugin) Call(ctx context.Context, export string, in sdk.Input, opts Options) Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := time.Now()
	store := sandbox.NewMemoryStore(opts.Storage)
	r := p.engine.Run(ctx, p.module, sandbox.Call{
		Export: export, Input: in, Store: store, HTTP: &script{responses: opts.Responses},
		Deadline: opts.Deadline, Record: true,
	})
	res := Result{Export: export, Input: in, Outcome: r.Outcome, Logs: r.Logs, Requests: r.Requests, Wakes: r.Wakes}
	if r.NoAnswer != nil {
		res.NoAnswer = r.NoAnswer.Detail
	}
	if values := store.Values(); len(values) > 0 {
		res.Storage = map[string]string{}
		for k, v := range values {
			res.Storage[k] = string(v)
		}
	}
	res.Duration, res.Runtime = time.Since(start), contract.Version()
	return res
}
