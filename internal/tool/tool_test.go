package tool_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-sdk/internal/tool"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// A new plugin builds, passes its own tests, validates and runs, as it is.
func TestNewPluginWorksAsMade(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "sms")
	res, err := tool.New(ctx, tool.NewRequest{Dir: dir, ID: "your-name/sms", SDK: repoRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tidy != "" {
		t.Fatalf("go mod tidy: %s", res.Tidy)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test in the new plugin: %v\n%s", err, out)
	}
	b, err := tool.Build(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(b.Bundle) != "sms-0.1.0.zip" || len(b.SHA256) != 64 || strings.Join(b.Exports, ",") != "on_event" {
		t.Fatalf("built %+v", b)
	}
	if r := tool.Validate(ctx, dir); !r.OK || !r.Built {
		t.Fatalf("validate %+v", r)
	}
	r, err := tool.Test(ctx, tool.TestRequest{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome == nil || r.Outcome.Kind != "ok" || len(r.Logs) != 1 || !strings.Contains(r.Logs[0], "F-1024") {
		t.Fatalf("test %+v", r)
	}

	if _, err := tool.New(ctx, tool.NewRequest{Dir: dir, ID: "your-name/sms", SDK: repoRoot(t)}); err == nil {
		t.Error("made a plugin over a folder that is not empty")
	}
	if _, err := tool.New(ctx, tool.NewRequest{Dir: t.TempDir() + "/x", ID: "No Slash", SDK: repoRoot(t)}); err == nil {
		t.Error("accepted a bad id")
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if d.IsDir() {
			if d.Name() == "dist" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The example webhook, built and run in the sandbox.
func TestExampleWebhook(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	copyDir(t, filepath.Join(repoRoot(t), "examples", "webhook"), dir)
	gomod := filepath.Join(dir, "go.mod")
	b, _ := os.ReadFile(gomod)
	os.WriteFile(gomod, []byte(strings.Replace(string(b), "=> ../..", "=> "+repoRoot(t), 1)), 0o644)

	if _, err := tool.Build(ctx, dir); err != nil {
		t.Fatal(err)
	}
	hook := map[string]string{"url": "https://hooks.example.com/in", "signing_secret": "s3cret"}

	r, err := tool.Test(ctx, tool.TestRequest{Dir: dir, Settings: hook})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome == nil || r.Outcome.Kind != "ok" || len(r.Requests) != 1 || r.Requests[0].Refused != "" ||
		r.Requests[0].Headers["X-Girvi-Signature"] == "" || !strings.Contains(r.Requests[0].Body, `"Asha Patel"`) {
		t.Fatalf("delivered: %+v", r)
	}

	r, _ = tool.Test(ctx, tool.TestRequest{Dir: dir, Event: "payment.received", Settings: hook, Responses: []tool.TestResponse{{Status: 503}}})
	if r.Outcome == nil || r.Outcome.Kind != "retry" || r.Outcome.RetryInSeconds != 300 {
		t.Fatalf("503: %+v", r)
	}

	// A plugin reaches only the host the user typed.
	gone := strings.Replace(string(mustRead(t, filepath.Join(dir, "plugin.json"))), `"{setting:url}"`, `"hooks.example.com"`, 1)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(gone), 0o644)
	r, _ = tool.Test(ctx, tool.TestRequest{Dir: dir, Settings: map[string]string{"url": "https://elsewhere.example.org/in"}})
	if len(r.Requests) != 1 || !strings.Contains(r.Requests[0].Refused, "not allowed to reach elsewhere.example.org") || r.Outcome == nil || r.Outcome.Kind != "retry" {
		t.Fatalf("an ungranted host: %+v", r)
	}

	// A plugin whose code makes network calls needs http_hosts.
	none := strings.Replace(gone, `"http_hosts": ["hooks.example.com"],`, ``, 1)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(none), 0o644)
	if rep := tool.Validate(ctx, dir); rep.OK || !strings.Contains(strings.Join(rep.Problems, " "), "http_hosts") {
		t.Fatalf("validate without http_hosts: %+v", rep)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
