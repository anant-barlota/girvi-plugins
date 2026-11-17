// Package harness runs a built plugin the way Girvi does, without Girvi: the
// same import checks at load, the same sandbox (no files, environment or
// arguments), the same limits, and the same rules for which hosts a request
// may reach. Network requests are not made; they are answered from a script
// and recorded, so a test is repeatable and never sends anything anywhere.
//
// It is a stand-in, not Girvi itself. Where they could differ, Girvi wins:
// tell the maintainers.
package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/manifest"
	"github.com/anant-barlota/girvi-plugins/sdk"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	wasiModule = "wasi_snapshot_preview1"
	wasmPage   = 64 << 10
)

// Plugin is a loaded plugin, ready to call.
type Plugin struct {
	Manifest manifest.Manifest
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex // one call at a time, as Girvi calls a plugin per firm
}

// Load checks a plugin's code against its manifest and compiles it, exactly
// as Girvi does at install. Nothing in the plugin runs.
func Load(ctx context.Context, manifestJSON, wasm []byte) (*Plugin, error) {
	m, err := manifest.Parse(manifestJSON)
	if err != nil {
		return nil, err
	}
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(contract.MemoryLimitMB << 20 / wasmPage)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	p := &Plugin{Manifest: m, rt: rt}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		p.Close(ctx)
		return nil, err
	}
	if err := hostFunctions(ctx, rt); err != nil {
		p.Close(ctx)
		return nil, err
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		p.Close(ctx)
		return nil, fmt.Errorf("plugin.wasm is not valid: %w", err)
	}
	p.compiled = compiled
	if err := m.CheckImports(compiled); err != nil {
		p.Close(ctx)
		return nil, err
	}
	return p, nil
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
func (p *Plugin) Exports() []string {
	var out []string
	for name := range p.compiled.ExportedFunctions() {
		if name == "on_event" || name == "on_wake" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (p *Plugin) Close(ctx context.Context) { _ = p.rt.Close(ctx) }

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

// Request is an HTTP request the plugin made.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	// Refused is why Girvi would not have made it; empty when allowed.
	Refused string `json:"refused,omitempty"`
}

// Wake is a wake-up the plugin asked for.
type Wake struct {
	At      time.Time `json:"at"` // after Girvi's clamping
	Payload string    `json:"payload"`
	Refused bool      `json:"refused,omitempty"`
}

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
}

// Settings is what a plugin is given for a firm: declared keys only, with
// defaults filled in.
func Settings(m manifest.Manifest, saved map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range m.Settings {
		if v, ok := saved[s.Key]; ok {
			out[s.Key] = v
		} else if s.Default != "" {
			out[s.Key] = s.Default
		}
	}
	return out
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
		Firm: sdk.Firm{ID: "test-firm", Name: "Test Firm"}, Settings: Settings(p.Manifest, saved),
	}, nil
}

// call is the state of one call, reached from host functions.
type call struct {
	manifest  manifest.Manifest
	input     []byte
	output    []byte
	result    []byte
	logs      []string
	requests  []Request
	responses []sdk.Response
	storage   map[string][]byte
	wakes     []Wake
	settings  map[string]string
}

type callKey struct{}

// Call runs one export ("on_event" or "on_wake") with an input.
func (p *Plugin) Call(ctx context.Context, export string, in sdk.Input, opts Options) Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := time.Now()
	res := Result{Export: export, Input: in}

	storage := map[string][]byte{}
	for k, v := range opts.Storage {
		storage[k] = v
	}
	raw, err := json.Marshal(in)
	if err != nil {
		res.NoAnswer = err.Error()
		return res
	}
	c := &call{manifest: p.Manifest, input: raw, responses: opts.Responses, storage: storage, settings: in.Settings}
	deadline := opts.Deadline
	if deadline == 0 {
		deadline = contract.CallDeadline
	}

	if err := p.run(ctx, export, c, deadline); err != nil {
		res.NoAnswer = err.Error()
	} else {
		var out sdk.Outcome
		if json.Unmarshal(c.output, &out) != nil || !slices.Contains([]string{"ok", "failed", "blocked", "retry"}, out.Kind) {
			res.NoAnswer = "it returned no outcome"
		} else {
			res.Outcome = &out
		}
	}
	res.Logs, res.Requests, res.Wakes = c.logs, c.requests, c.wakes
	if len(c.storage) > 0 {
		res.Storage = map[string]string{}
		for k, v := range c.storage {
			res.Storage[k] = string(v)
		}
	}
	res.Duration = time.Since(start)
	return res
}

func (p *Plugin) run(ctx context.Context, export string, c *call, deadline time.Duration) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the harness hit a problem running it: %v", r)
		}
	}()
	if _, ok := p.compiled.ExportedFunctions()[export]; !ok {
		return fmt.Errorf("it has no %s function", export)
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, callKey{}, c), deadline)
	defer cancel()

	stderr := &cappedBuffer{limit: contract.MaxStderr}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithStderr(stderr).WithStdout(stderr).
		WithSysWalltime().WithSysNanotime().
		WithNanosleep(func(ns int64) {
			select {
			case <-time.After(time.Duration(ns)):
			case <-ctx.Done():
			}
		}).
		WithRandSource(rand.Reader)

	inst, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if err != nil {
		return callError(ctx, err, stderr, deadline)
	}
	defer inst.Close(context.Background())
	if _, err := inst.ExportedFunction(export).Call(ctx); err != nil {
		return callError(ctx, err, stderr, deadline)
	}
	return nil
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func callError(ctx context.Context, err error, stderr *cappedBuffer, deadline time.Duration) error {
	if ctx.Err() != nil {
		return fmt.Errorf("it ran longer than %s", deadline)
	}
	detail, _, _ := strings.Cut(strings.TrimSpace(stderr.buf.String()), "\n")
	if detail == "" {
		detail = err.Error()
		var exit *sys.ExitError
		if errors.As(err, &exit) {
			detail = fmt.Sprintf("it exited with code %d", exit.ExitCode())
		}
	}
	return errors.New(truncate(detail, contract.MaxLogLine))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func read(m api.Module, ptr, n uint32) []byte {
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		panic("the plugin passed a pointer outside its memory")
	}
	return bytes.Clone(b)
}

func write(m api.Module, ptr uint32, b []byte) {
	if !m.Memory().Write(ptr, b) {
		panic("the plugin passed a pointer outside its memory")
	}
}

func storageSize(s map[string][]byte) int64 {
	var n int64
	for k, v := range s {
		n += int64(len(k) + len(v))
	}
	return n
}

func hostFunctions(ctx context.Context, rt wazero.Runtime) error {
	state := func(ctx context.Context) *call { return ctx.Value(callKey{}).(*call) }
	b := rt.NewHostModuleBuilder(contract.HostModule)

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context) uint32 {
		return uint32(len(state(ctx).input))
	}).Export("input_len")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr uint32) {
		write(m, ptr, state(ctx).input)
	}).Export("input_read")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		state(ctx).output = read(m, ptr, n)
	}).Export("output")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr uint32) {
		write(m, ptr, state(ctx).result)
	}).Export("result_read")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		c := state(ctx)
		if len(c.logs) < contract.MaxLogLines {
			c.logs = append(c.logs, truncate(string(read(m, ptr, min(n, contract.MaxLogLine+1))), contract.MaxLogLine))
		}
	}).Export("log")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) uint32 {
		c := state(ctx)
		c.result, _ = json.Marshal(c.httpRequest(read(m, ptr, n)))
		return uint32(len(c.result))
	}).Export("http_request")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn uint32) int32 {
		c := state(ctx)
		if kn > contract.MaxStorageKey {
			return contract.HostError
		}
		v, ok := c.storage[string(read(m, kp, kn))]
		if !ok {
			return contract.HostMissing
		}
		c.result = v
		return int32(len(v))
	}).Export("storage_get")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn, vp, vn uint32) int32 {
		c := state(ctx)
		if kn == 0 || kn > contract.MaxStorageKey || vn > contract.MaxStorageValue {
			return contract.HostError
		}
		// As in Girvi, an overwrite is counted as if it were new.
		if len(c.storage) >= contract.MaxStorageKeys || storageSize(c.storage)+int64(kn)+int64(vn) > contract.MaxStorageBytes {
			return contract.HostError
		}
		c.storage[string(read(m, kp, kn))] = read(m, vp, vn)
		return contract.HostOK
	}).Export("storage_set")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn uint32) int32 {
		c := state(ctx)
		if kn > contract.MaxStorageKey {
			return contract.HostError
		}
		delete(c.storage, string(read(m, kp, kn)))
		return contract.HostOK
	}).Export("storage_delete")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, unixSeconds int64, pp, pn uint32) int32 {
		c := state(ctx)
		pending := 0
		for _, w := range c.wakes {
			if !w.Refused {
				pending++
			}
		}
		if pn > contract.MaxWakePayload || pending >= contract.MaxWakeUps {
			c.wakes = append(c.wakes, Wake{Payload: truncate(string(read(m, pp, min(pn, contract.MaxLogLine+1))), contract.MaxLogLine), Refused: true})
			return contract.HostError
		}
		now := time.Now()
		earliest, latest := now.Add(contract.MinWakeDelay), now.Add(contract.MaxDelay)
		at := earliest
		if unixSeconds > latest.Unix() {
			at = latest
		} else if unixSeconds > earliest.Unix() {
			at = time.Unix(unixSeconds, 0)
		}
		c.wakes = append(c.wakes, Wake{At: at.UTC(), Payload: string(read(m, pp, pn))})
		return contract.HostOK
	}).Export("wake_at")

	_, err := b.Instantiate(ctx)
	return err
}

type httpRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

func (c *call) httpRequest(raw []byte) sdk.Response {
	var r httpRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return sdk.Response{Error: "bad request: " + err.Error()}
	}
	rec := Request{Method: r.Method, URL: r.URL, Headers: r.Headers, Body: string(r.Body)}
	refuse := func(why string) sdk.Response {
		rec.Refused = why
		c.requests = append(c.requests, rec)
		return sdk.Response{Error: why}
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return refuse("not an http or https URL")
	}
	// Girvi also checks the address a granted name resolves to; with no
	// network here, there is nothing to resolve.
	if granted, _ := c.manifest.HostGranted(c.settings, u); !granted {
		return refuse("this plugin is not allowed to reach " + u.Host)
	}
	c.requests = append(c.requests, rec)
	if len(c.responses) == 0 {
		return sdk.Response{Status: 200}
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	if len(resp.Body) > contract.MaxHTTPResponse {
		resp.Body = resp.Body[:contract.MaxHTTPResponse]
	}
	return resp
}
