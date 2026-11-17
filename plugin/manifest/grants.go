package manifest

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/anant-barlota/girvi-sdk/plugin/contract"
	"github.com/tetratelabs/wazero"
)

const wasiModule = "wasi_snapshot_preview1"

// alwaysGranted are host functions every plugin may import. http_request is
// not here: it needs http_hosts in the manifest.
var alwaysGranted = map[string]bool{
	"input_len": true, "input_read": true, "output": true, "result_read": true, "log": true,
	"storage_get": true, "storage_set": true, "storage_delete": true, "wake_at": true,
}

// CheckImports refuses compiled code that imports something its manifest does
// not grant, or that Girvi could never call. Nothing in the plugin runs.
func (m Manifest) CheckImports(compiled wazero.CompiledModule) error {
	for _, f := range compiled.ImportedFunctions() {
		mod, name, _ := f.Import()
		switch {
		case mod == wasiModule:
		case mod == contract.HostModule && alwaysGranted[name]:
		case mod == contract.HostModule && name == "http_request":
			if len(m.HTTPHosts) == 0 {
				return errors.New("the plugin makes network calls but its plugin.json lists no http_hosts")
			}
		default:
			return fmt.Errorf("the plugin asks for %s.%s, which Girvi does not provide", mod, name)
		}
	}
	if _, ok := compiled.ExportedFunctions()["on_event"]; !ok {
		return errors.New("plugin.wasm has no on_event function, so Girvi could never call it")
	}
	return nil
}

// HostGranted reports whether the manifest's http_hosts covers u, and whether
// the grant reaches this computer or network on purpose (local). An entry is:
//   - "{setting:KEY}": the exact host and port of the URL the user typed into
//     that setting. The user chose it, so it may be local.
//   - "name" or "name:port": that host, on any port or on that one. It may be
//     local only if the entry itself names a local address.
//   - "*.name": any subdomain. Never local.
//
// Girvi also checks the address a granted name resolves to, at connect time:
// a grant that is not local never reaches this computer or network.
func (m Manifest) HostGranted(settings map[string]string, u *url.URL) (granted, local bool) {
	name := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, entry := range m.HTTPHosts {
		if key, ok := SettingHost(entry); ok {
			s, err := url.Parse(strings.TrimSpace(settings[key]))
			if err == nil && s.Hostname() != "" && hostPort(s) == hostPort(u) {
				return true, true
			}
			continue
		}
		if suffix, ok := strings.CutPrefix(entry, "*"); ok {
			if strings.HasSuffix(name, suffix) {
				granted = true
			}
			continue
		}
		entryName, entryPort, _ := strings.Cut(entry, ":")
		if entryName == name && (entryPort == "" || name+":"+entryPort == hostPort(u)) {
			if LocalName(entryName) {
				return true, true
			}
			granted = true
		}
	}
	return granted, false
}

// hostPort is a URL's host as "name:port", lower case, with the scheme's
// default port filled in, so "example.com" and "example.com:443" compare equal.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") + ":" + port
}
