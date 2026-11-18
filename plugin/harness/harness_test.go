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
	"github.com/anant-barlota/girvi-sdk/plugin/contract"
	"github.com/anant-barlota/girvi-sdk/plugin/harness"
)

var (
	buildOnce sync.Once
	hostile   *harness.Plugin
	buildErr  error
)

func load(t *testing.T) *harness.Plugin {
	t.Helper()
	buildOnce.Do(func() {
		dir := filepath.Join("testdata", "hostile")
		var b *tool.Built
		if b, buildErr = tool.Build(context.Background(), dir); buildErr != nil {
			return
		}
		defer os.RemoveAll(filepath.Join(dir, "dist"))
		raw, _ := os.ReadFile(filepath.Join(dir, "plugin.json"))
		wasm, _ := os.ReadFile(b.Wasm)
		hostile, buildErr = harness.Load(context.Background(), raw, wasm)
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return hostile
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
	// The hostile plugin does not ask for customer details, so it is not given them.
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
		// Reading outside its own memory ends the call, not the host.
		"bad-pointer": "pointer outside its memory",
		// An outcome over the limit is not even read.
		"huge-outcome": "outcome is larger than 64 KB",
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
	raw, _ := os.ReadFile(filepath.Join("testdata", "hostile", "plugin.json"))
	if _, err := harness.Load(context.Background(), raw, []byte("\x00asm\x01\x00\x00\x00")); err == nil || !strings.Contains(err.Error(), "no on_event") {
		t.Errorf("an empty module: %v", err)
	}
	if _, err := harness.Load(context.Background(), []byte(`{"id":"a/b","name":"B","version":"1","api":"girvi/v2"}`), nil); err == nil {
		t.Error("another API version was accepted")
	}
}

// The limits a plugin meets when it pushes past them on purpose.
func TestHostileLimits(t *testing.T) {
	note := func(mode string) (harness.Result, string) {
		t.Helper()
		r := call(t, mode, 0)
		if r.Outcome == nil {
			t.Fatalf("%s: no outcome: %s", mode, r.NoAnswer)
		}
		return r, r.Outcome.Note
	}

	if _, n := note("big-storage"); n != "long key=false big value=false empty key=false" {
		t.Errorf("storage over the limits: %s", n)
	}

	before := time.Now()
	r, n := note("wakes")
	if n != "now=true never=true big payload=false" || len(r.Wakes) != 3 || !r.Wakes[2].Refused {
		t.Fatalf("wake-ups: %s %+v", n, r.Wakes)
	}
	if soon := r.Wakes[0].At.Sub(before); soon < contract.MinWakeDelay-time.Second || soon > contract.MinWakeDelay+5*time.Second {
		t.Errorf("a wake-up asked for in the past is %s away, want %s", soon, contract.MinWakeDelay)
	}
	if late := r.Wakes[1].At.Sub(before); late > contract.MaxDelay+5*time.Second || late < contract.MaxDelay-5*time.Second {
		t.Errorf("a wake-up asked for in the far future is %s away, want %s", late, contract.MaxDelay)
	}

	r, _ = note("logs")
	if len(r.Logs) != contract.MaxLogLines {
		t.Errorf("%d log lines kept, want %d", len(r.Logs), contract.MaxLogLines)
	}
	for _, l := range r.Logs {
		if len(l) > contract.MaxLogLine+len("…") {
			t.Fatalf("a log line of %d bytes was kept", len(l))
		}
	}

	if _, n := note("long-note"); len(n) != contract.MaxNote+len("…") {
		t.Errorf("a note of %d bytes was kept, want %d", len(n), contract.MaxNote)
	}

	// The last outcome counts; reading a result before any is harmless.
	if r, n := note("twice"); r.Outcome.Kind != "ok" || n != "second" {
		t.Errorf("two outcomes: %+v", r.Outcome)
	}
	if _, n := note("no-result"); n != "read nothing" {
		t.Errorf("result_read first: %s", n)
	}

	r, n = note("elsewhere")
	if !strings.Contains(n, "not allowed to reach attacker.example.net") || len(r.Requests) != 1 || r.Requests[0].Refused == "" {
		t.Errorf("a host not in plugin.json: %s %+v", n, r.Requests)
	}

	// What a crashing plugin printed is cut to one short line.
	r = call(t, "stderr", 0)
	if r.Outcome != nil || !strings.HasPrefix(r.NoAnswer, "xxxx") || len(r.NoAnswer) > contract.MaxLogLine+len("…")+len("crash: ") {
		t.Errorf("a crash after 100 KB of stderr: %d bytes: %.80s", len(r.NoAnswer), r.NoAnswer)
	}
}
