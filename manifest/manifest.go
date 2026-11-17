// Package manifest reads and checks plugin.json and plugin bundles, with the
// same rules Girvi applies when a plugin is installed.
package manifest

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// APIVersion is the contract this package describes.
const APIVersion = "girvi/1"

// Events a plugin can subscribe to.
const (
	EventLoanCreated     = "loan.created"
	EventPaymentReceived = "payment.received"
	EventLoanRedeemed    = "loan.redeemed"
)

var knownEvents = []string{EventLoanCreated, EventPaymentReceived, EventLoanRedeemed}

// Personal details a plugin receives only if its manifest asks for them.
const (
	FieldCustomerName  = "customer.name"
	FieldCustomerPhone = "customer.phone"
)

var knownFields = []string{FieldCustomerName, FieldCustomerPhone}

// What happens to a call that was interrupted (crash, quit, power loss), when
// Girvi cannot know whether the plugin's side effect happened.
const (
	AtLeastOnce = "at_least_once" // called again: a duplicate is possible
	AtMostOnce  = "at_most_once"  // marked failed for a manual Send again: a loss is possible
)

// Manifest is plugin.json: everything Girvi knows about a plugin before running it.
type Manifest struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	API         string    `json:"api"`
	Description string    `json:"description,omitempty"`
	Author      string    `json:"author,omitempty"`
	Events      []string  `json:"events,omitempty"`
	Fields      []string  `json:"fields,omitempty"`
	HTTPHosts   []string  `json:"http_hosts,omitempty"`
	Settings    []Setting `json:"settings,omitempty"`
	Delivery    string    `json:"delivery,omitempty"`
}

// Setting is one field of a plugin's settings form. Settings are per firm.
type Setting struct {
	Key         string `json:"key"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"` // string (default) | boolean
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     string `json:"default,omitempty"`
	Advanced    bool   `json:"advanced,omitempty"`
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*$`)
	keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// A host name or IPv4 address with an optional port, or a wildcard over
	// the subdomains of a name with at least two labels ("*.example.com", never
	// "*.com").
	hostPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	wildcardPattern = regexp.MustCompile(`^\*(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?){2,}$`)
)

const settingHostPrefix = "{setting:"

// SettingHost returns the key in a "{setting:KEY}" host entry: the host of the
// URL the user entered in that setting.
func SettingHost(entry string) (key string, ok bool) {
	if !strings.HasPrefix(entry, settingHostPrefix) || !strings.HasSuffix(entry, "}") {
		return "", false
	}
	return entry[len(settingHostPrefix) : len(entry)-1], true
}

// Parse reads and checks plugin.json. Unknown fields are refused: a
// grant this host does not understand must not be silently dropped.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("plugin.json: %w", err)
	}
	if m.API != APIVersion {
		return m, fmt.Errorf("plugin.json: this plugin needs %q, and this version of Girvi provides %q", m.API, APIVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return m, fmt.Errorf("plugin.json: id must look like publisher/name in lower case, got %q", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" {
		return m, fmt.Errorf("plugin.json: name and version are required")
	}
	for _, e := range m.Events {
		if !slices.Contains(knownEvents, e) {
			return m, fmt.Errorf("plugin.json: unknown event %q", e)
		}
	}
	for _, f := range m.Fields {
		if !slices.Contains(knownFields, f) {
			return m, fmt.Errorf("plugin.json: unknown field %q", f)
		}
	}
	keys := map[string]bool{}
	for _, s := range m.Settings {
		if !keyPattern.MatchString(s.Key) || keys[s.Key] {
			return m, fmt.Errorf("plugin.json: setting key %q is invalid or repeated", s.Key)
		}
		if s.Type != "" && s.Type != "string" && s.Type != "boolean" {
			return m, fmt.Errorf("plugin.json: setting %q has unknown type %q", s.Key, s.Type)
		}
		keys[s.Key] = true
	}
	for _, h := range m.HTTPHosts {
		if key, ok := SettingHost(h); ok {
			i := slices.IndexFunc(m.Settings, func(s Setting) bool { return s.Key == key })
			if i < 0 {
				return m, fmt.Errorf("plugin.json: http_hosts names setting %q, which is not declared", key)
			}
			// The grant is for a host the user typed, never one the author chose.
			if m.Settings[i].Default != "" {
				return m, fmt.Errorf("plugin.json: setting %q is used in http_hosts, so it cannot have a default", key)
			}
			continue
		}
		if wildcardPattern.MatchString(h) {
			continue
		}
		if !hostPattern.MatchString(h) {
			return m, fmt.Errorf("plugin.json: http_hosts entry %q must be a host name, or *.name with at least two labels", h)
		}
		// An address on this computer or network reaches whatever else runs
		// there, Girvi's own API included, so it has to name one port.
		if name, port, _ := strings.Cut(h, ":"); port == "" && LocalName(name) {
			return m, fmt.Errorf("plugin.json: http_hosts entry %q is on this computer or network, so it must name a port, like %s:9000", h, name)
		}
	}
	switch m.Delivery {
	case "":
		m.Delivery = AtLeastOnce
	case AtLeastOnce, AtMostOnce:
	default:
		return m, fmt.Errorf("plugin.json: unknown delivery %q", m.Delivery)
	}
	return m, nil
}

// Wants reports whether the plugin subscribes to an event.
func (m Manifest) Wants(event string) bool { return slices.Contains(m.Events, event) }

// Bundle is an installable plugin: a zip holding plugin.json and plugin.wasm.
type Bundle struct {
	Manifest Manifest
	Wasm     []byte
	Hash     string // SHA-256 of the whole zip; approval is for exactly this
}

const (
	maxWasmSize     = 64 << 20
	maxManifestSize = 64 << 10
)

// ReadBundle opens a bundle without running anything in it.
func ReadBundle(zipBytes []byte) (*Bundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("not a plugin bundle: %w", err)
	}
	read := func(name string, limit int64) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, fmt.Errorf("the bundle has no %s", name)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s is too large", name)
		}
		return b, nil
	}
	raw, err := read("plugin.json", maxManifestSize)
	if err != nil {
		return nil, err
	}
	manifest, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	wasm, err := read("plugin.wasm", maxWasmSize)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(zipBytes)
	return &Bundle{Manifest: manifest, Wasm: wasm, Hash: hex.EncodeToString(sum[:])}, nil
}

// LocalName reports whether a host name or address is this computer or a
// private network outright. Such an http_hosts entry must name a port.
func LocalName(name string) bool {
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || strings.HasSuffix(name, ".local") {
		return true
	}
	addr, err := netip.ParseAddr(name)
	return err == nil && LocalAddr(addr)
}

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// LocalAddr reports whether an address is on this computer or a private network.
func LocalAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() || carrierGradeNAT.Contains(addr)
}
