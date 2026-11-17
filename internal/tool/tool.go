// Package tool is what the girvi-plugin CLI and the MCP server do: make a new
// plugin, check it, build it into a bundle, and run it against sample events.
// Both front ends call these functions and print or return their results.
package tool

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/harness"
	"github.com/anant-barlota/girvi-plugins/manifest"
	"github.com/anant-barlota/girvi-plugins/sdk"
)

// ModulePath is this repository's Go module, which plugins depend on for the SDK.
const ModulePath = contract.ModulePath

// SDKVersion is the version of this module the running program was built
// with, or "" for a build from a checkout.
func SDKVersion() string { return contract.Version() }

// NewRequest describes a plugin to make.
type NewRequest struct {
	Dir         string `json:"dir" jsonschema:"folder to create; must not exist or be empty"`
	ID          string `json:"id" jsonschema:"plugin id, publisher/name in lower case, for example your-name/sms"`
	Name        string `json:"name,omitempty" jsonschema:"name shown to the user; defaults to the name part of the id"`
	Description string `json:"description,omitempty" jsonschema:"one sentence shown to the user"`
	// SDK is a local checkout of girvi-plugins to use in place of a released
	// version (a replace directive). Needed when this program was itself built
	// from a checkout.
	SDK string `json:"sdk,omitempty" jsonschema:"path to a local girvi-plugins checkout to use instead of a released SDK"`
}

// NewResult is what New made.
type NewResult struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"`
	// Tidy is the output of go mod tidy when it failed; the files are still
	// made, and it can be run again by hand.
	Tidy string   `json:"tidy_error,omitempty"`
	Next []string `json:"next"`
}

type scaffold struct {
	ID, Name, Short, Description, Module, Require, Replace string
}

var scaffoldFiles = map[string]string{
	"plugin.json": `{
  "id": "{{.ID}}",
  "name": "{{.Name}}",
  "version": "0.1.0",
  "api": "girvi/1",
  "description": "{{.Description}}",
  "events": ["loan.created"]
}
`,
	"go.mod": `module {{.Module}}

go 1.25.0

require {{.Require}}
{{- if .Replace}}

replace github.com/anant-barlota/girvi-plugins => {{.Replace}}
{{- end}}
`,
	"main.go": `//go:build wasip1

// The exports Girvi calls. Everything else is in handler.go, which has no
// WebAssembly in it, so it is tested with plain go test.
package main

import "github.com/anant-barlota/girvi-plugins/sdk"

//go:wasmexport on_event
func onEvent() { sdk.Run(handle) }

func main() {}
`,
	"handler.go": `package main

import "github.com/anant-barlota/girvi-plugins/sdk"

// handle is called for each event listed in plugin.json "events", and for Send
// test. It decides what happened: sdk.OK, sdk.Failed, sdk.Blocked (a setting
// needs fixing) or sdk.Retry. See the contract: girvi-plugin contract.
func handle(h sdk.Host, in sdk.Input) sdk.Outcome {
	switch in.EventType() {
	case sdk.EventLoanCreated:
		e, err := in.LoanCreated()
		if err != nil {
			return sdk.Failed(err.Error())
		}
		h.Log("loan " + e.Loan.Number + " for " + e.Loan.Principal.Display)
		return sdk.OK("")
	}
	return sdk.Failed("{{.Short}} does not handle " + in.EventType())
}
`,
	"handler_test.go": `package main

import (
	"testing"

	"github.com/anant-barlota/girvi-plugins/sdk"
	"github.com/anant-barlota/girvi-plugins/sdk/sdktest"
)

func TestLoanCreated(t *testing.T) {
	h := sdktest.New()
	out := handle(h, sdktest.Event(t, sdk.EventLoanCreated, nil))
	if out.Kind != "ok" {
		t.Fatalf("outcome %+v", out)
	}
	if len(h.Logs) != 1 {
		t.Fatalf("logs %v", h.Logs)
	}
}
`,
	".gitignore": "dist/\n",
	"README.md": `# {{.Name}}

A Girvi plugin.

    go test ./...            # handler logic
    girvi-plugin build       # dist/{{.Short}}-<version>.zip, the bundle to install
    girvi-plugin test        # run the built plugin against a sample event

Install the zip from Girvi's Plugins screen. Nothing runs until it is approved.
`,
}

// New makes a plugin folder that builds and passes its tests as it is.
func New(ctx context.Context, req NewRequest) (*NewResult, error) {
	probe, err := json.Marshal(map[string]string{"id": req.ID, "name": "x", "version": "x", "api": manifest.APIVersion})
	if err != nil {
		return nil, err
	}
	if _, err := manifest.Parse(probe); err != nil {
		return nil, err
	}
	if req.Dir == "" {
		return nil, errors.New("say which folder to make")
	}
	if entries, err := os.ReadDir(req.Dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s is not empty", req.Dir)
	}
	_, short, _ := strings.Cut(req.ID, "/")
	s := scaffold{ID: req.ID, Name: req.Name, Short: short, Description: req.Description, Module: req.ID}
	if s.Name == "" {
		s.Name = short
	}
	if s.Description == "" {
		s.Description = "A Girvi plugin."
	}
	switch v := SDKVersion(); {
	case req.SDK != "":
		abs, err := filepath.Abs(req.SDK)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(abs, "sdk", "sdk.go")); err != nil {
			return nil, fmt.Errorf("%s is not a girvi-plugins checkout", req.SDK)
		}
		s.Require, s.Replace = ModulePath+" v0.0.0", filepath.ToSlash(abs)
	case v != "":
		s.Require = ModulePath + " " + v
	default:
		return nil, errors.New("this girvi-plugin was built from a local checkout, so it does not know which SDK version to use. Pass the checkout's path as the SDK (--sdk)")
	}

	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return nil, err
	}
	res := &NewResult{Dir: req.Dir}
	for name, text := range scaffoldFiles {
		var buf bytes.Buffer
		if err := template.Must(template.New(name).Parse(text)).Execute(&buf, s); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(req.Dir, name), buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, name)
	}
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = req.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		res.Tidy = strings.TrimSpace(string(out))
	}
	res.Next = []string{
		"Edit plugin.json: the events it hears, the customer fields it needs, the hosts it may reach, its settings.",
		"Edit handler.go, and test it with: go test ./...",
		"Build the bundle with: girvi-plugin build",
		"Run it against a sample event with: girvi-plugin test",
	}
	return res, nil
}

// Report is what Validate found.
type Report struct {
	OK       bool               `json:"ok"`
	Manifest *manifest.Manifest `json:"manifest,omitempty"`
	// Built is false when there is no dist/plugin.wasm yet; then only the
	// manifest was checked.
	Built    bool     `json:"built"`
	Exports  []string `json:"exports,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

func readManifest(dir string) (manifest.Manifest, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return manifest.Manifest{}, nil, fmt.Errorf("no plugin.json in %s", dir)
	}
	m, err := manifest.Parse(raw)
	return m, raw, err
}

// Validate checks plugin.json, and the built plugin.wasm if there is one,
// with Girvi's install rules.
func Validate(ctx context.Context, dir string) Report {
	r := Report{}
	m, raw, err := readManifest(dir)
	if err != nil {
		r.Problems = append(r.Problems, err.Error())
		return r
	}
	r.Manifest = &m
	wasm, err := os.ReadFile(filepath.Join(dir, "dist", "plugin.wasm"))
	if err == nil {
		r.Built = true
		p, err := harness.Load(ctx, raw, wasm)
		if err != nil {
			r.Problems = append(r.Problems, err.Error())
		} else {
			r.Exports = p.Exports()
			p.Close(ctx)
		}
	} else {
		r.Notes = append(r.Notes, "dist/plugin.wasm is not built yet, so only plugin.json was checked. Run build.")
	}
	if strings.HasPrefix(m.ID, "girvi/") {
		r.Notes = append(r.Notes, "girvi/ ids are reserved for official plugins; a catalog will refuse this one from anyone else.")
	}
	if len(m.Events) == 0 {
		r.Notes = append(r.Notes, "plugin.json lists no events, so Girvi will only call it for Send test.")
	}
	r.OK = len(r.Problems) == 0
	return r
}

// Built is what Build made.
type Built struct {
	Wasm    string   `json:"wasm"`
	Bundle  string   `json:"bundle"`
	SHA256  string   `json:"sha256"` // of the bundle: what Girvi's approval is for
	Size    int64    `json:"size"`
	Exports []string `json:"exports"`
}

// Build compiles the plugin to WebAssembly, checks it as Girvi would at
// install, and zips it with plugin.json into dist/<name>-<version>.zip.
func Build(ctx context.Context, dir string) (*Built, error) {
	// go build runs inside dir, so every path given to it must be absolute.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	m, raw, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return nil, err
	}
	wasmPath := filepath.Join(dist, "plugin.wasm")
	cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-trimpath", "-ldflags=-s -w", "-o", wasmPath, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build failed:\n%s", strings.TrimSpace(string(out)))
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, err
	}
	p, err := harness.Load(ctx, raw, wasm)
	if err != nil {
		return nil, err
	}
	exports := p.Exports()
	p.Close(ctx)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{{"plugin.json", raw}, {"plugin.wasm", wasm}} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	_, short, _ := strings.Cut(m.ID, "/")
	bundle := filepath.Join(dist, short+"-"+m.Version+".zip")
	if err := os.WriteFile(bundle, buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return &Built{Wasm: wasmPath, Bundle: bundle, SHA256: hex.EncodeToString(sum[:]), Size: int64(buf.Len()), Exports: exports}, nil
}

// TestRequest describes one call to make against a built plugin.
type TestRequest struct {
	Dir string `json:"dir" jsonschema:"the plugin folder; it must be built"`
	// Event is the sample event to deliver: loan.created, payment.received or
	// loan.redeemed. Defaults to the first event in plugin.json.
	Event string `json:"event,omitempty" jsonschema:"loan.created, payment.received or loan.redeemed; defaults to the first in plugin.json"`
	// Kind is event (default), test (as Send test sends it) or wake (calls
	// on_wake with Wake as its payload).
	Kind     string            `json:"kind,omitempty" jsonschema:"event (default), test, or wake"`
	Wake     string            `json:"wake,omitempty" jsonschema:"payload for a wake call"`
	Settings map[string]string `json:"settings,omitempty" jsonschema:"this firm's settings, by key"`
	// Responses answer the plugin's allowed HTTP requests in order; after
	// them, requests get 200. Nothing is sent over the network.
	Responses []TestResponse    `json:"responses,omitempty" jsonschema:"answers to the plugin's HTTP requests, in order; after them requests get status 200"`
	Storage   map[string]string `json:"storage,omitempty" jsonschema:"what the plugin's storage holds before the call"`
}

// TestResponse is a scripted HTTP answer.
type TestResponse struct {
	Status int    `json:"status,omitempty" jsonschema:"HTTP status"`
	Body   string `json:"body,omitempty" jsonschema:"response body"`
	Error  string `json:"error,omitempty" jsonschema:"set to pretend the request got no answer, for example a timeout"`
}

// Test runs the built plugin once, as Girvi would, and reports everything it did.
func Test(ctx context.Context, req TestRequest) (*harness.Result, error) {
	_, raw, err := readManifest(req.Dir)
	if err != nil {
		return nil, err
	}
	wasm, err := os.ReadFile(filepath.Join(req.Dir, "dist", "plugin.wasm"))
	if err != nil {
		return nil, errors.New("dist/plugin.wasm is not built yet. Run build first")
	}
	p, err := harness.Load(ctx, raw, wasm)
	if err != nil {
		return nil, err
	}
	defer p.Close(ctx)

	opts := harness.Options{Storage: map[string][]byte{}}
	for k, v := range req.Storage {
		opts.Storage[k] = []byte(v)
	}
	for _, r := range req.Responses {
		opts.Responses = append(opts.Responses, sdk.Response{Status: r.Status, Body: []byte(r.Body), Error: r.Error})
	}

	kind := req.Kind
	if kind == "" {
		kind = sdk.KindEvent
	}
	if kind == sdk.KindWake {
		in := sdk.Input{Kind: sdk.KindWake, DeliveryID: "wake-1", Attempt: 1, Wake: req.Wake,
			Firm: sdk.Firm{ID: "test-firm", Name: "Test Firm"}, Settings: harness.Settings(p.Manifest, req.Settings)}
		res := p.Call(ctx, "on_wake", in, opts)
		return &res, nil
	}
	if kind != sdk.KindEvent && kind != sdk.KindTest {
		return nil, fmt.Errorf("kind must be event, test or wake, not %q", kind)
	}
	event := req.Event
	if event == "" {
		if len(p.Manifest.Events) == 0 {
			return nil, errors.New("plugin.json lists no events; name one to send")
		}
		event = p.Manifest.Events[0]
	}
	in, err := p.Input(event, kind, req.Settings)
	if err != nil {
		return nil, err
	}
	res := p.Call(ctx, "on_event", in, opts)
	return &res, nil
}
