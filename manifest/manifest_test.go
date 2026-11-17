package manifest_test

import (
	"archive/zip"
	"bytes"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-plugins/manifest"
)

func TestParse(t *testing.T) {
	for _, valid := range []string{
		`{"id":"a/b","name":"B","version":"1","api":"girvi/v1"}`,
		`{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["api.example.com","*.example.com","localhost:9000","192.168.1.10:8080"]}`,
		`{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["{setting:url}"],"settings":[{"key":"url"}]}`,
	} {
		if _, err := manifest.Parse([]byte(valid)); err != nil {
			t.Errorf("%s: %v", valid, err)
		}
	}
	for name, bad := range map[string]string{
		"another api version":     `{"id":"a/b","name":"B","version":"1","api":"girvi/2"}`,
		"id without publisher":    `{"id":"b","name":"B","version":"1","api":"girvi/v1"}`,
		"id with a path":          `{"id":"a/../b","name":"B","version":"1","api":"girvi/v1"}`,
		"unknown event":           `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","events":["loan.deleted"]}`,
		"unknown field":           `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","fields":["customer.documents"]}`,
		"unknown grant":           `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","filesystem":true}`,
		"host with a path":        `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["example.com/x"]}`,
		"wildcard over a TLD":     `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["*.com"]}`,
		"local with no port":      `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["localhost"]}`,
		"private with no port":    `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["192.168.1.10"]}`,
		"undeclared setting host": `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["{setting:url}"]}`,
		"setting host default":    `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","http_hosts":["{setting:url}"],"settings":[{"key":"url","default":"https://x.example"}]}`,
		"repeated setting":        `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","settings":[{"key":"a"},{"key":"a"}]}`,
		"unknown delivery":        `{"id":"a/b","name":"B","version":"1","api":"girvi/v1","delivery":"exactly_once"}`,
	} {
		if _, err := manifest.Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	m, _ := manifest.Parse([]byte(`{"id":"a/b","name":"B","version":"1","api":"girvi/v1"}`))
	if m.Delivery != manifest.AtLeastOnce {
		t.Errorf("default delivery %q", m.Delivery)
	}
}

func bundle(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestReadBundle(t *testing.T) {
	good := `{"id":"a/b","name":"B","version":"1","api":"girvi/v1"}`
	b, err := manifest.ReadBundle(bundle(t, map[string]string{"plugin.json": good, "plugin.wasm": "x"}))
	if err != nil || b.Manifest.ID != "a/b" || len(b.Hash) != 64 {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := manifest.ReadBundle(bundle(t, map[string]string{"plugin.json": good})); err == nil {
		t.Error("a bundle with no plugin.wasm was read")
	}
	huge := strings.Replace(good, `"name":"B"`, `"name":"B","description":"`+strings.Repeat("x", 70<<10)+`"`, 1)
	if _, err := manifest.ReadBundle(bundle(t, map[string]string{"plugin.json": huge, "plugin.wasm": "x"})); err == nil {
		t.Error("an oversized plugin.json was read")
	}
}

func TestHostGrants(t *testing.T) {
	settings := map[string]string{"url": "https://hooks.shop.example/in", "tally": "http://localhost:9000"}
	entries := []string{"api.example.com", "*.cdn.example.com", "localhost:9100", "{setting:url}", "{setting:tally}"}
	for target, want := range map[string][2]bool{ // granted, local
		"https://api.example.com/x":            {true, false},
		"https://API.Example.com./x":           {true, false},
		"https://api.example.com:8443/x":       {true, false},
		"https://a.cdn.example.com/x":          {true, false},
		"https://cdn.example.com/x":            {false, false}, // a wildcard covers subdomains only
		"https://evilcdn.example.com/x":        {false, false},
		"https://api.example.com.evil.io/x":    {false, false},
		"https://user@evil.io/api.example.com": {false, false},
		"https://hooks.shop.example/other":     {true, true},
		"https://hooks.shop.example:443/in":    {true, true},
		"http://hooks.shop.example/in":         {false, false}, // another port
		"http://localhost:9000/tally":          {true, true},
		"http://localhost:9100/x":              {true, true},
		"http://localhost:8081/customers":      {false, false}, // not the port the user or the manifest named
		"http://127.0.0.1:9000/x":              {false, false},
	} {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		granted, local := (manifest.Manifest{HTTPHosts: entries}).HostGranted(settings, u)
		if granted != want[0] || local != want[1] {
			t.Errorf("%s: granted=%v local=%v, want %v", target, granted, local, want)
		}
	}
}

func TestLocalAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.5": true, "172.20.0.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "::ffff:127.0.0.1": true, "0.0.0.0": true, "fe80::1": true,
		"8.8.8.8": false, "2606:4700::1111": false,
	} {
		if got := manifest.LocalAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: local=%v, want %v", addr, got, want)
		}
	}
}
