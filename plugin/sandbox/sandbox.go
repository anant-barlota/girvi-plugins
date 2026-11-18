// Package sandbox runs a plugin: Girvi's own runtime, and the one the test
// harness uses, so the two cannot behave differently. It compiles a plugin,
// checks its imports against its manifest, and runs one call in a fresh
// instance with the girvi_v1 host functions, the limits, the deadline and the
// rules for what counts as an answer.
//
// What a call touches outside the plugin is given to it: a Store for the
// plugin's storage and wake-ups, and a Sender for HTTP. Girvi passes ones
// backed by its database and the network; the harness passes ones in memory.
package sandbox

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
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin"
	"github.com/anant-barlota/girvi-sdk/plugin/contract"
	"github.com/anant-barlota/girvi-sdk/plugin/manifest"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const wasmPage = 64 << 10

// Store is a plugin's storage and wake-ups for one firm. The sandbox checks
// every limit before calling it; a Store only keeps and counts.
type Store interface {
	// Get reports false when the key has no value.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Set adds or replaces a value.
	Set(ctx context.Context, key string, value []byte) error
	// Delete removes a value; a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// Usage is how many keys are held and the sum of their key and value
	// lengths, in bytes.
	Usage(ctx context.Context) (keys, size int64, err error)
	// PendingWakeUps is how many wake-ups are scheduled and not yet had.
	PendingWakeUps(ctx context.Context) (int64, error)
	// WakeAt schedules a wake-up, at a time the sandbox has already clamped.
	WakeAt(ctx context.Context, at time.Time, payload string) error
}

// Sender makes an HTTP request the manifest allows. local reports whether the
// grant may reach this computer or network; Network enforces it.
type Sender interface {
	Send(ctx context.Context, r plugin.Request, local bool) plugin.Response
}

// Engine is one WebAssembly runtime with the host functions. Girvi shares one
// between every plugin.
type Engine struct {
	rt wazero.Runtime
}

// NewEngine starts a runtime. cache may be nil.
func NewEngine(ctx context.Context, cache wazero.CompilationCache) (*Engine, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true). // lets the deadline stop a call that never returns
		WithMemoryLimitPages(contract.MemoryLimitMB << 20 / wasmPage)
	if cache != nil {
		cfg = cfg.WithCompilationCache(cache)
	}
	e := &Engine{rt: wazero.NewRuntimeWithConfig(ctx, cfg)}
	// A Go plugin's runtime needs WASI for its clock, random numbers and stderr.
	// Each instance gets no files, no arguments and no environment.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, e.rt); err != nil {
		e.Close(ctx)
		return nil, err
	}
	if err := hostFunctions(ctx, e.rt); err != nil {
		e.Close(ctx)
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close(ctx context.Context) { _ = e.rt.Close(ctx) }

// Module is a compiled plugin, checked against its manifest.
type Module struct {
	Manifest manifest.Manifest
	compiled wazero.CompiledModule
}

// Compile checks a plugin's code against its manifest and compiles it. Nothing
// in the plugin runs.
func (e *Engine) Compile(ctx context.Context, m manifest.Manifest, wasm []byte) (*Module, error) {
	compiled, err := e.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("plugin.wasm is not valid: %w", err)
	}
	if err := m.CheckImports(compiled); err != nil {
		_ = compiled.Close(ctx)
		return nil, err
	}
	return &Module{Manifest: m, compiled: compiled}, nil
}

// Exports lists the functions the plugin exports for Girvi.
func (m *Module) Exports() []string {
	var out []string
	for name := range m.compiled.ExportedFunctions() {
		if name == "on_event" || name == "on_wake" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (m *Module) Close(ctx context.Context) { _ = m.compiled.Close(ctx) }

// Call is one call into a plugin.
type Call struct {
	Export string // on_event or on_wake
	Input  plugin.Input
	Store  Store
	HTTP   Sender
	// Deadline is how long the call may run; zero means contract.CallDeadline.
	Deadline time.Duration
	// Record keeps every HTTP request and wake-up the plugin asked for in the
	// Result, refused or not. For tests: it holds request bodies in memory.
	Record bool
}

// Request is an HTTP request the plugin made, when recording.
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	// Refused is why it was not sent; empty when it was allowed.
	Refused string `json:"refused,omitempty"`
}

// Wake is a wake-up the plugin asked for, when recording.
type Wake struct {
	At      time.Time `json:"at"` // after clamping
	Payload string    `json:"payload"`
	Refused bool      `json:"refused,omitempty"`
}

// NoAnswer is a call that ended without an outcome.
type NoAnswer struct {
	Reason string // timeout | crash | no_outcome
	Detail string
}

func (e *NoAnswer) Error() string { return e.Detail }

// Result is how a call ended. Exactly one of Outcome and NoAnswer is set.
type Result struct {
	Outcome  *plugin.Outcome
	NoAnswer *NoAnswer
	Logs     []string
	Requests []Request // only when recording
	Wakes    []Wake    // only when recording
}

// call is the state of one call, reached from the host functions.
type call struct {
	Call
	settings map[string]string
	manifest manifest.Manifest
	requests int // http_request calls so far
	input    []byte
	output   []byte
	result   []byte // the last host function result, waiting for result_read
	res      *Result
}

type callKey struct{}

// Run calls one export in a fresh instance, which is discarded afterwards.
// Whatever the plugin does to its own memory dies with it.
func (e *Engine) Run(ctx context.Context, m *Module, c Call) Result {
	res := Result{}
	raw, err := json.Marshal(c.Input)
	if err != nil {
		res.NoAnswer = &NoAnswer{Reason: "crash", Detail: err.Error()}
		return res
	}
	st := &call{Call: c, settings: c.Input.Settings, manifest: m.Manifest, input: raw, res: &res}
	if err := e.run(ctx, m, st); err != nil {
		res.NoAnswer = err
		return res
	}
	var out plugin.Outcome
	if json.Unmarshal(st.output, &out) != nil || !slices.Contains([]string{"ok", "failed", "blocked", "retry"}, out.Kind) {
		res.NoAnswer = &NoAnswer{Reason: "no_outcome", Detail: "it returned no outcome"}
		return res
	}
	res.Outcome = &out
	return res
}

func (e *Engine) run(ctx context.Context, m *Module, c *call) (err *NoAnswer) {
	// Plugins run inside the app, so a panic on this path must end the call,
	// never the app.
	defer func() {
		if r := recover(); r != nil {
			err = &NoAnswer{Reason: "crash", Detail: fmt.Sprintf("Girvi hit a problem running it: %v", r)}
		}
	}()
	if _, ok := m.compiled.ExportedFunctions()[c.Export]; !ok {
		return &NoAnswer{Reason: "crash", Detail: fmt.Sprintf("it has no %s function", c.Export)}
	}
	deadline := c.Deadline
	if deadline == 0 {
		deadline = contract.CallDeadline
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, callKey{}, c), deadline)
	defer cancel()

	stderr := &cappedBuffer{limit: contract.MaxStderr}
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous, so calls can run side by side
		WithStartFunctions("_initialize").
		WithStderr(stderr).WithStdout(stderr).
		WithSysWalltime().WithSysNanotime().
		// A sleeping plugin wakes when the call's deadline passes or the app
		// closes; a plain sleep could not be interrupted.
		WithNanosleep(func(ns int64) {
			select {
			case <-time.After(time.Duration(ns)):
			case <-ctx.Done():
			}
		}).
		WithRandSource(rand.Reader)

	inst, ierr := e.rt.InstantiateModule(ctx, m.compiled, cfg)
	if ierr != nil {
		return callError(ctx, ierr, stderr, deadline)
	}
	defer inst.Close(context.Background())
	if _, cerr := inst.ExportedFunction(c.Export).Call(ctx); cerr != nil {
		return callError(ctx, cerr, stderr, deadline)
	}
	return nil
}

// cappedBuffer keeps the first limit bytes written and drops the rest, so a
// plugin cannot use its output to exhaust the host's memory.
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

func callError(ctx context.Context, err error, stderr *cappedBuffer, deadline time.Duration) *NoAnswer {
	if ctx.Err() != nil {
		return &NoAnswer{Reason: "timeout", Detail: fmt.Sprintf("it ran longer than %s", deadline)}
	}
	// A Go plugin prints its panic or out-of-memory message first.
	detail, _, _ := strings.Cut(strings.TrimSpace(stderr.buf.String()), "\n")
	if detail == "" {
		detail = err.Error()
		var exit *sys.ExitError
		if errors.As(err, &exit) {
			detail = fmt.Sprintf("it exited with code %d", exit.ExitCode())
		}
	}
	return &NoAnswer{Reason: "crash", Detail: truncate(detail, contract.MaxLogLine)}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// read copies bytes out of the plugin's memory. A bad pointer ends the call.
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

// WakeTime is when a wake-up asked for at unixSeconds happens: no sooner than
// contract.MinWakeDelay from now and no later than contract.MaxDelay. Clamped
// before any arithmetic, so a huge value cannot wrap around.
func WakeTime(now time.Time, unixSeconds int64) time.Time {
	earliest, latest := now.Add(contract.MinWakeDelay), now.Add(contract.MaxDelay)
	switch {
	case unixSeconds > latest.Unix():
		return latest
	case unixSeconds > earliest.Unix():
		return time.Unix(unixSeconds, 0)
	}
	return earliest
}

// RetryDelay is how long Girvi waits before calling again after a retry
// outcome: the plugin's delay, at least a second and at most contract.MaxDelay.
func RetryDelay(o plugin.Outcome) time.Duration {
	s := min(max(int64(o.RetryInSeconds), 1), int64(contract.MaxDelay/time.Second))
	return time.Duration(s) * time.Second
}

// hostFunctions defines girvi_v1: the whole surface a plugin can reach. Every
// call moves one message each way, and the plugin pulls and pushes the bytes
// itself, so the host never allocates in a plugin's memory or calls back into
// it.
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
		if len(c.res.Logs) < contract.MaxLogLines {
			c.res.Logs = append(c.res.Logs, truncate(string(read(m, ptr, min(n, contract.MaxLogLine+1))), contract.MaxLogLine))
		}
	}).Export("log")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) uint32 {
		c := state(ctx)
		c.requests++
		var resp plugin.Response
		switch {
		case c.requests > contract.MaxHTTPRequests:
			resp = plugin.Response{Error: fmt.Sprintf("more than %d requests in one call", contract.MaxHTTPRequests)}
		case n > maxRequestJSON:
			resp = plugin.Response{Error: "the request body is larger than 1 MB"}
		default:
			resp = c.httpRequest(ctx, read(m, ptr, n))
		}
		c.result, _ = json.Marshal(resp)
		return uint32(len(c.result))
	}).Export("http_request")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn uint32) int32 {
		c := state(ctx)
		if kn > contract.MaxStorageKey {
			return contract.HostError
		}
		value, found, err := c.Store.Get(ctx, string(read(m, kp, kn)))
		if err != nil {
			return contract.HostError
		}
		if !found {
			return contract.HostMissing
		}
		c.result = value
		return int32(len(value))
	}).Export("storage_get")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn, vp, vn uint32) int32 {
		c := state(ctx)
		if kn == 0 || kn > contract.MaxStorageKey || vn > contract.MaxStorageValue {
			return contract.HostError
		}
		// Approximate on purpose: an overwrite is counted as if it were new.
		keys, size, err := c.Store.Usage(ctx)
		if err != nil || keys >= contract.MaxStorageKeys || size+int64(kn)+int64(vn) > contract.MaxStorageBytes {
			return contract.HostError
		}
		if err := c.Store.Set(ctx, string(read(m, kp, kn)), read(m, vp, vn)); err != nil {
			return contract.HostError
		}
		return contract.HostOK
	}).Export("storage_set")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn uint32) int32 {
		c := state(ctx)
		if kn > contract.MaxStorageKey {
			return contract.HostError
		}
		if err := c.Store.Delete(ctx, string(read(m, kp, kn))); err != nil {
			return contract.HostError
		}
		return contract.HostOK
	}).Export("storage_delete")

	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, unixSeconds int64, pp, pn uint32) int32 {
		c := state(ctx)
		refuse := func() int32 {
			if c.Record {
				c.res.Wakes = append(c.res.Wakes, Wake{Payload: truncate(string(read(m, pp, min(pn, contract.MaxLogLine+1))), contract.MaxLogLine), Refused: true})
			}
			return contract.HostError
		}
		if pn > contract.MaxWakePayload {
			return refuse()
		}
		if n, err := c.Store.PendingWakeUps(ctx); err != nil || n >= contract.MaxWakeUps {
			return refuse()
		}
		at, payload := WakeTime(time.Now(), unixSeconds), string(read(m, pp, pn))
		if err := c.Store.WakeAt(ctx, at, payload); err != nil {
			return refuse()
		}
		if c.Record {
			c.res.Wakes = append(c.res.Wakes, Wake{At: at.UTC(), Payload: payload})
		}
		return contract.HostOK
	}).Export("wake_at")

	_, err := b.Instantiate(ctx)
	return err
}

// maxRequestJSON is the most http_request reads: a body at the limit, base64
// encoded in the request's JSON, with room for the method, URL and headers.
const maxRequestJSON = contract.MaxHTTPRequest*4/3 + 64<<10

func (c *call) httpRequest(ctx context.Context, raw []byte) plugin.Response {
	var r plugin.Request
	if err := json.Unmarshal(raw, &r); err != nil {
		return plugin.Response{Error: "bad request: " + err.Error()}
	}
	rec := Request{Method: r.Method, URL: r.URL, Headers: r.Headers, Body: string(r.Body)}
	refuse := func(why string) plugin.Response {
		if c.Record {
			rec.Refused = why
			c.res.Requests = append(c.res.Requests, rec)
		}
		return plugin.Response{Error: why}
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return refuse("not an http or https URL")
	}
	if len(r.Body) > contract.MaxHTTPRequest {
		return refuse("the request body is larger than 1 MB")
	}
	granted, local := c.manifest.HostGranted(c.settings, u)
	if !granted {
		return refuse("this plugin is not allowed to reach " + u.Host)
	}
	if c.Record {
		c.res.Requests = append(c.res.Requests, rec)
	}
	resp := c.HTTP.Send(ctx, r, local)
	if len(resp.Body) > contract.MaxHTTPResponse {
		resp.Body = resp.Body[:contract.MaxHTTPResponse]
	}
	return resp
}
