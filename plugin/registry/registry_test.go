package registry

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const goodURL = "https://github.com/acme/hook/releases/download/v1.0.0/hook-1.0.0.zip"

func good() *Index {
	return &Index{Schema: Schema, Name: "Test", Plugins: []Entry{{
		ID: "acme/hook", Name: "Hook", Author: "Acme", Description: "does a thing",
		Versions: []Version{{Version: "1.0.0", API: "girvi/v1", Events: []string{"loan.created"},
			Bundle: Bundle{URL: goodURL, SHA256: strings.Repeat("a", 64), Size: 10}}},
	}}}
}

func has(problems []string, part string) bool {
	for _, p := range problems {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

func TestAGoodIndexHasNoProblems(t *testing.T) {
	if p := Validate(good(), Policy{OwnerMustMatchID: true}); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	idx := good()
	idx.Schema = 2
	idx.Plugins[0].ID = "Acme/Hook"
	idx.Plugins = append(idx.Plugins, Entry{ID: "acme/two", Versions: []Version{{Version: "v1.0", API: "3",
		Bundle: Bundle{URL: "http://x/y", SHA256: "short"}}}})
	p := Validate(idx, Policy{})
	for _, want := range []string{"schema must be 1", "not a valid plugin id", "needs a name", "needs a author", "needs a description", "no leading v", "api must be", "64 lowercase hex", "size must be", "https"} {
		if !has(p, want) {
			t.Errorf("missing a problem mentioning %q in %v", want, p)
		}
	}
}

func TestAnAuthorCannotListABundleUnderAnotherAuthorsId(t *testing.T) {
	idx := good()
	idx.Plugins[0].ID = "someone-else/hook"
	if p := Validate(idx, Policy{OwnerMustMatchID: true}); !has(p, "account allowed for the plugin id") {
		t.Errorf("a bundle from another account must be refused, got %v", p)
	}
	if p := Validate(idx, Policy{}); has(p, "account allowed for the plugin id") {
		t.Error("the check must be switchable off for a private catalog")
	}
}

func TestDuplicatesAreRefused(t *testing.T) {
	idx := good()
	idx.Plugins[0].Versions = append(idx.Plugins[0].Versions, idx.Plugins[0].Versions[0])
	idx.Plugins = append(idx.Plugins, idx.Plugins[0])
	p := Validate(idx, Policy{})
	if !has(p, "acme/hook is listed twice") || !has(p, "acme/hook 1.0.0 is listed twice") {
		t.Errorf("want both duplicates in %v", p)
	}
}

func TestAPublishedVersionCannotBeChangedButCanBeAddedOrRemoved(t *testing.T) {
	old := good()
	if p := CheckImmutable(old, good()); len(p) != 0 {
		t.Errorf("unchanged: %v", p)
	}
	swapped := good()
	swapped.Plugins[0].Versions[0].Bundle.SHA256 = strings.Repeat("b", 64)
	if p := CheckImmutable(old, swapped); !has(p, "cannot be changed") {
		t.Errorf("a swapped bundle must be refused, got %v", p)
	}
	widened := good()
	widened.Plugins[0].Versions[0].HTTPHosts = []string{"evil.example"}
	if p := CheckImmutable(old, widened); !has(p, "cannot be changed") {
		t.Errorf("widening what a published version asks for must be refused, got %v", p)
	}
	added := good()
	added.Plugins[0].Versions = append(added.Plugins[0].Versions, Version{Version: "1.1.0"})
	if p := CheckImmutable(old, added); len(p) != 0 {
		t.Errorf("adding a version is allowed: %v", p)
	}
	removed := good()
	removed.Plugins[0].Versions = nil
	if p := CheckImmutable(old, removed); len(p) != 0 {
		t.Errorf("removing a bad version is allowed: %v", p)
	}
}

func TestAMisspelledKeyIsAnErrorNotAMissingChecksum(t *testing.T) {
	_, err := Parse([]byte(`{"schema":1,"name":"x","plugins":[{"id":"a/b","name":"B","versions":[{"version":"1.0.0","api":"girvi/v1","bundle":{"url":"u","sha265":"x"}}]}]}`))
	if err == nil || !strings.Contains(err.Error(), "sha265") {
		t.Errorf("got %v", err)
	}
}

func TestAddListsNewPluginsAndVersionsOnce(t *testing.T) {
	idx := &Index{Schema: Schema, Name: "Test"}
	v := good().Plugins[0].Versions[0]
	e := Entry{ID: "acme/hook", Name: "Hook", Author: "Acme", Description: "does a thing", License: "MIT"}
	if err := Add(idx, e, v); err != nil {
		t.Fatal(err)
	}
	if err := Add(idx, e, v); err == nil || !strings.Contains(err.Error(), "already listed") {
		t.Errorf("listing a version twice: %v", err)
	}
	next := v
	next.Version = "1.1.0"
	e.Description, e.License = "does it better", ""
	if err := Add(idx, e, next); err != nil {
		t.Fatal(err)
	}
	p := idx.Plugins[0]
	if len(idx.Plugins) != 1 || len(p.Versions) != 2 || p.Description != "does it better" || p.License != "MIT" {
		t.Errorf("after a second version: %+v", idx.Plugins)
	}
	if problems := Validate(idx, Policy{OwnerMustMatchID: true}); len(problems) != 0 {
		t.Errorf("problems: %v", problems)
	}
}

func TestTheCatalogIsWrittenInAStableOrder(t *testing.T) {
	idx := good()
	idx.Plugins = append([]Entry{{ID: "zed/last", Versions: []Version{{Version: "1.10.0"}, {Version: "1.2.0"}}}}, idx.Plugins...)
	out := string(Canonical(idx))
	if strings.Index(out, "acme/hook") > strings.Index(out, "zed/last") {
		t.Error("plugins must be sorted by id")
	}
	if strings.Index(out, `"1.2.0"`) > strings.Index(out, `"1.10.0"`) {
		t.Error("versions must be sorted as versions, not as text")
	}
	if string(Canonical(idx)) != out {
		t.Error("the same catalog must always produce the same bytes")
	}
}

func bundle(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"plugin.json": manifest, "plugin.wasm": "\x00asm"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBundlesAreCheckedAgainstTheCatalog(t *testing.T) {
	zipped := bundle(t, `{"id":"acme/hook","name":"Hook","version":"1.0.0","api":"girvi/v1","events":["loan.created"]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/not-a-bundle":
			_, _ = w.Write([]byte("a program"))
		default:
			_, _ = w.Write(zipped)
		}
	}))
	defer srv.Close()
	sum := sha256.Sum256(zipped)
	listed := func(path string) *Index {
		idx := good()
		idx.Plugins[0].Versions[0].Bundle = Bundle{URL: srv.URL + path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(zipped))}
		return idx
	}
	verify := func(idx *Index) []string { return VerifyBundles(context.Background(), srv.Client(), idx) }

	if p := verify(listed("/ok")); len(p) != 0 {
		t.Errorf("a correct entry: %v", p)
	}
	idx := listed("/ok")
	idx.Plugins[0].Versions[0].Bundle.SHA256 = strings.Repeat("0", 64)
	idx.Plugins[0].Versions[0].Bundle.Size = 3
	if p := verify(idx); !has(p, "sha256 is") || !has(p, "size is") {
		t.Errorf("wrong checksum and size: %v", p)
	}
	idx = listed("/ok")
	idx.Plugins[0].ID = "acme/other"
	idx.Plugins[0].Versions[0].Version = "1.0.1"
	if p := verify(idx); !has(p, `says id "acme/hook"`) || !has(p, `says version "1.0.0"`) {
		t.Errorf("a bundle of another plugin or version: %v", p)
	}
	idx = listed("/ok")
	idx.Plugins[0].Versions[0].Events = nil
	idx.Plugins[0].Versions[0].HTTPHosts = []string{"api.example.com"}
	if p := verify(idx); !has(p, "events are [loan.created]") || !has(p, "http_hosts are [] in the bundle") {
		t.Errorf("grants that differ from the bundle's: %v", p)
	}
	if p := verify(listed("/missing")); !has(p, "HTTP 404") {
		t.Errorf("missing bundle: %v", p)
	}
	if p := verify(listed("/not-a-bundle")); !has(p, "not a plugin bundle") {
		t.Errorf("not a bundle: %v", p)
	}
}

func TestTheOfficialNamespaceCanOnlyBeHostedByTheMaintainer(t *testing.T) {
	idx := good()
	idx.Plugins[0].ID = "girvi/whatsapp"
	idx.Plugins[0].Versions[0].Bundle.URL = "https://github.com/anant-barlota/girvi-sdk/releases/download/whatsapp/v1.0.0/whatsapp-1.0.0.zip"
	if p := Validate(idx, Policy{OwnerMustMatchID: true}); len(p) != 0 {
		t.Errorf("the maintainer may host girvi/ plugins: %v", p)
	}
	idx.Plugins[0].Versions[0].Bundle.URL = "https://github.com/girvi/whatsapp/releases/download/v1.0.0/whatsapp-1.0.0.zip"
	if p := Validate(idx, Policy{OwnerMustMatchID: true}); !has(p, "allowed for the plugin id") {
		t.Errorf("anyone else claiming girvi/ must be refused, got %v", p)
	}
}

// A path that starts with an allowed account must not be able to walk into
// another account's repository.
func TestBundlesCannotEscapeTheAllowedAccount(t *testing.T) {
	policy := Policy{OwnerMustMatchID: true}
	if msg := checkBundleURL(goodURL, "acme", policy); msg != "" {
		t.Fatalf("a real release asset must pass: %s", msg)
	}
	if msg := checkBundleURL("https://github.com/acme/hook/releases/download/whatsapp/v1.0.0/asset", "acme", policy); msg != "" {
		t.Errorf("a tag with a slash is allowed: %s", msg)
	}
	for name, raw := range map[string]string{
		"dot-dot segments":      "https://github.com/acme/hook/releases/download/v1/../../../../attacker/evil/releases/download/v1/bin",
		"encoded dot-dot":       "https://github.com/acme/%2e%2e/attacker/evil/releases/download/v1/bin",
		"mixed-case encoding":   "https://github.com/acme/hook/releases/download/v1/%2E%2e/bin",
		"a dot segment":         "https://github.com/acme/hook/releases/download/v1/./bin",
		"backslash":             "https://github.com/acme/hook/releases/download/v1\\..\\bin",
		"empty segment":         "https://github.com/acme/hook/releases/download//bin",
		"too short":             "https://github.com/acme/hook/releases/download/v1",
		"not a release":         "https://github.com/acme/hook/archive/refs/heads/main.zip",
		"a query string":        goodURL + "?x=1",
		"a fragment":            goodURL + "#x",
		"a port":                "https://github.com:8443/acme/hook/releases/download/v1/bin",
		"another account":       "https://github.com/attacker/hook/releases/download/v1/bin",
		"another host":          "https://evil.example/acme/hook/releases/download/v1/bin",
		"a lookalike subdomain": "https://github.com.evil.example/acme/hook/releases/download/v1/bin",
	} {
		if msg := checkBundleURL(raw, "acme", policy); msg == "" {
			t.Errorf("%s must be refused: %s", name, raw)
		}
	}
}
