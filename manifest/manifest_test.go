package manifest_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-plugins/manifest"
)

func TestParse(t *testing.T) {
	for _, valid := range []string{
		`{"id":"a/b","name":"B","version":"1","api":"girvi/1"}`,
		`{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["api.example.com","*.example.com","localhost:9000","192.168.1.10:8080"]}`,
		`{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["{setting:url}"],"settings":[{"key":"url"}]}`,
	} {
		if _, err := manifest.Parse([]byte(valid)); err != nil {
			t.Errorf("%s: %v", valid, err)
		}
	}
	for name, bad := range map[string]string{
		"another api version":     `{"id":"a/b","name":"B","version":"1","api":"girvi/2"}`,
		"id without publisher":    `{"id":"b","name":"B","version":"1","api":"girvi/1"}`,
		"id with a path":          `{"id":"a/../b","name":"B","version":"1","api":"girvi/1"}`,
		"unknown event":           `{"id":"a/b","name":"B","version":"1","api":"girvi/1","events":["loan.deleted"]}`,
		"unknown field":           `{"id":"a/b","name":"B","version":"1","api":"girvi/1","fields":["customer.documents"]}`,
		"unknown grant":           `{"id":"a/b","name":"B","version":"1","api":"girvi/1","filesystem":true}`,
		"host with a path":        `{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["example.com/x"]}`,
		"wildcard over a TLD":     `{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["*.com"]}`,
		"local with no port":      `{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["localhost"]}`,
		"undeclared setting host": `{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["{setting:url}"]}`,
		"setting host default":    `{"id":"a/b","name":"B","version":"1","api":"girvi/1","http_hosts":["{setting:url}"],"settings":[{"key":"url","default":"https://x.example"}]}`,
		"repeated setting":        `{"id":"a/b","name":"B","version":"1","api":"girvi/1","settings":[{"key":"a"},{"key":"a"}]}`,
		"unknown delivery":        `{"id":"a/b","name":"B","version":"1","api":"girvi/1","delivery":"exactly_once"}`,
	} {
		if _, err := manifest.Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	m, _ := manifest.Parse([]byte(`{"id":"a/b","name":"B","version":"1","api":"girvi/1"}`))
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
	good := `{"id":"a/b","name":"B","version":"1","api":"girvi/1"}`
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
