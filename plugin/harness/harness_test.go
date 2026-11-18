package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anant-barlota/girvi-sdk/internal/tool"
	"github.com/anant-barlota/girvi-sdk/plugin"
	"github.com/anant-barlota/girvi-sdk/plugin/harness"
)

var (
	buildOnce sync.Once
	probe     *harness.Plugin
	buildErr  error
)

func load(t *testing.T) *harness.Plugin {
	t.Helper()
	buildOnce.Do(func() {
		dir := filepath.Join("testdata", "probe")
		var b *tool.Built
		if b, buildErr = tool.Build(context.Background(), dir); buildErr != nil {
			return
		}
		defer os.RemoveAll(filepath.Join(dir, "dist"))
		raw, _ := os.ReadFile(filepath.Join(dir, "plugin.json"))
		wasm, _ := os.ReadFile(b.Wasm)
		probe, buildErr = harness.Load(context.Background(), raw, wasm)
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return probe
}

func call(t *testing.T, mode string, deadline time.Duration) harness.Result {
	t.Helper()
	p := load(t)
	in, err := p.Input(plugin.EventLoanCreated, plugin.KindEvent, map[string]string{"mode": mode})
	if err != nil {
		t.Fatal(err)
	}
	return p.Call(context.Background(), "on_event", in, harness.Options{Deadline: deadline})
}

func TestOrdinaryCall(t *testing.T) {
	r := call(t, "", 0)
	if r.Outcome == nil || r.Outcome.Kind != "ok" || len(r.Logs) != 1 || r.Logs[0] != "hello" {
		t.Fatalf("%+v", r)
	}
	// The probe does not ask for customer details, so it is not given them.
	if strings.Contains(string(r.Input.Event), "Asha") || strings.Contains(string(r.Input.Event), "phone") {
		t.Errorf("customer details reached a plugin that did not ask: %s", r.Input.Event)
	}
}

func TestBrokenPluginsGiveNoAnswer(t *testing.T) {
	for mode, want := range map[string]string{
		"panic": "panic: plugin bug",
		"spin":  "ran longer than 1s",
		"sleep": "ran longer than 1s",
		"hog":   "out of memory",
	} {
		deadline := time.Duration(0) // Girvi's
		if mode == "spin" || mode == "sleep" {
			deadline = time.Second
		}
		r := call(t, mode, deadline)
		if r.Outcome != nil || !strings.Contains(r.NoAnswer, want) {
			t.Errorf("%s: outcome %+v, no answer %q", mode, r.Outcome, r.NoAnswer)
		}
	}
	// And the next call is clean.
	if r := call(t, "", 0); r.Outcome == nil || r.Outcome.Kind != "ok" {
		t.Fatalf("after the broken calls: %+v", r)
	}
}

func TestSandboxAndLimits(t *testing.T) {
	if r := call(t, "files", 0); r.Outcome == nil || r.Outcome.Note != "read=false env=0" {
		t.Fatalf("files: %+v", r)
	}
	r := call(t, "store", 0)
	if r.Outcome == nil || r.Outcome.Note != "stored=1000 wake=true" || len(r.Wakes) != 1 {
		t.Fatalf("store: %+v", r)
	}
	r = call(t, "flood", 0)
	if r.Outcome == nil || r.Outcome.Note != "sent=20 last=more than 20 requests in one call" || len(r.Requests) != 20 {
		t.Fatalf("flood: outcome %+v, %d requests", r.Outcome, len(r.Requests))
	}
	r = call(t, "big", 0)
	if r.Outcome == nil || r.Outcome.Note != `1MB="" 2MB="the request body is larger than 1 MB"` || len(r.Requests) != 1 {
		t.Fatalf("big: outcome %+v, %d requests", r.Outcome, len(r.Requests))
	}
}

func TestWake(t *testing.T) {
	p := load(t)
	r := p.Call(context.Background(), "on_wake", plugin.Input{Kind: plugin.KindWake, Wake: "later"}, harness.Options{})
	if r.Outcome == nil || r.Outcome.Note != "woke with later" {
		t.Fatalf("%+v", r)
	}
}

func TestLoadRefuses(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "probe", "plugin.json"))
	if _, err := harness.Load(context.Background(), raw, []byte("\x00asm\x01\x00\x00\x00")); err == nil || !strings.Contains(err.Error(), "no on_event") {
		t.Errorf("an empty module: %v", err)
	}
	if _, err := harness.Load(context.Background(), []byte(`{"id":"a/b","name":"B","version":"1","api":"girvi/2"}`), nil); err == nil {
		t.Error("another API version was accepted")
	}
}
