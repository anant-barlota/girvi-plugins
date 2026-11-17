package tool_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-sdk/internal/tool"
	"github.com/anant-barlota/girvi-sdk/plugin/registry"
)

// releases answers GitHub release downloads from memory.
type releases map[string][]byte

func (r releases) RoundTrip(req *http.Request) (*http.Response, error) {
	body, ok := r[req.URL.String()]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

func zipped(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"plugin.json": manifest, "plugin.wasm": "\x00asm"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPublishListsTheReleasedBundle(t *testing.T) {
	ctx := context.Background()
	const manifest = `{"id":"acme/hook","name":"Hook","version":"0.2.0","api":"girvi/v1","description":"Posts loans.","author":"Acme","events":["loan.created"],"http_hosts":["hooks.example.com"]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(index, []byte(`{"schema":1,"name":"Test","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const url = "https://github.com/acme/hook/releases/download/v0.2.0/hook-0.2.0.zip"
	c := &http.Client{Transport: releases{url: zipped(t, manifest)}}
	req := tool.PublishRequest{Dir: dir, Repo: "acme/hook", Index: index, License: "MIT"}

	p, err := tool.Publish(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(index)
	idx, err := registry.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	e := idx.Plugins[0]
	v := e.Versions[0]
	if p.Bundle.URL != url || e.Author != "Acme" || e.Homepage != "https://github.com/acme/hook" || e.License != "MIT" ||
		v.API != "girvi/v1" || strings.Join(v.HTTPHosts, ",") != "hooks.example.com" || v.Bundle.SHA256 != p.Bundle.SHA256 {
		t.Fatalf("listed %+v", idx.Plugins)
	}

	if _, err := tool.Publish(ctx, c, req); err == nil || !strings.Contains(err.Error(), "already listed") {
		t.Errorf("publishing the same version again: %v", err)
	}
	other := zipped(t, strings.Replace(manifest, `"0.2.0"`, `"0.1.0"`, 1))
	req.Tag = "v0.3.0"
	c.Transport = releases{"https://github.com/acme/hook/releases/download/v0.3.0/hook-0.2.0.zip": other}
	if _, err := tool.Publish(ctx, c, req); err == nil || !strings.Contains(err.Error(), "acme/hook 0.1.0, but plugin.json says acme/hook 0.2.0") {
		t.Errorf("a release of another version: %v", err)
	}
	req.Tag = "v9"
	if _, err := tool.Publish(ctx, c, req); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("no such release: %v", err)
	}
}
