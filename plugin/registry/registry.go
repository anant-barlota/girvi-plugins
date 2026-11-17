// Package registry is the plugin catalog Girvi reads under Plugins, Browse
// (index.json), and the rules a catalog must follow.
//
// A catalog holds no programs. Each version points at a plugin bundle, the zip
// that girvi plugin build makes, released by its author, with the bundle's
// SHA-256. It repeats what the bundle's plugin.json asks for, so Girvi can show
// that before downloading anything; VerifyBundles checks the two agree.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Schema is the catalog format this package reads and writes. Schema 1 listed
// process plugins, one program per platform; schema 2 lists bundles.
const Schema = 2

type Index struct {
	Schema  int     `json:"schema"`
	Name    string  `json:"name"`
	Plugins []Entry `json:"plugins"`
}

type Entry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Author      string    `json:"author"`
	Description string    `json:"description"`
	Homepage    string    `json:"homepage,omitempty"`
	License     string    `json:"license,omitempty"`
	Versions    []Version `json:"versions"`
}

// Version is one release. API, Events, Fields and HTTPHosts are copied from
// the bundle's plugin.json.
type Version struct {
	Version   string   `json:"version"`
	API       string   `json:"api"`
	Events    []string `json:"events,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	HTTPHosts []string `json:"http_hosts,omitempty"`
	Bundle    Bundle   `json:"bundle"`
}

type Bundle struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// MaxBundleSize is the largest download a catalog may point at.
const MaxBundleSize = 128 << 20

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,48}/[a-z0-9][a-z0-9-]{0,48}$`)
	apiPattern    = regexp.MustCompile(`^girvi/v[1-9][0-9]*$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Parse reads a catalog and refuses fields it does not know, so a typo in a
// key (for example "sha265") is an error and not a silently missing checksum.
func Parse(data []byte) (*Index, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("index.json is not valid: %w", err)
	}
	if head.Schema != Schema {
		return nil, fmt.Errorf("index.json is schema %d; this version reads schema %d", head.Schema, Schema)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("index.json is not valid: %w", err)
	}
	return &idx, nil
}

// Policy decides who may list what.
type Policy struct {
	// OwnerMustMatchID requires every bundle to be a GitHub release asset of the
	// account named at the start of the plugin id. It stops one author listing
	// a bundle under another author's id.
	OwnerMustMatchID bool
}

// Validate returns every problem it finds, so an author can fix them in one go.
// It reads only the catalog; VerifyBundles downloads what it points at.
func Validate(idx *Index, p Policy) []string {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if idx.Schema != Schema {
		bad("schema must be %d, not %d", Schema, idx.Schema)
	}
	if strings.TrimSpace(idx.Name) == "" {
		bad("the catalog needs a name")
	}
	seen := map[string]bool{}
	for _, e := range idx.Plugins {
		if !idPattern.MatchString(e.ID) {
			bad("%q is not a valid plugin id (owner/name, lowercase letters, digits and dashes)", e.ID)
			continue
		}
		if seen[e.ID] {
			bad("%s is listed twice", e.ID)
		}
		seen[e.ID] = true
		for _, f := range []struct{ name, value string }{{"name", e.Name}, {"author", e.Author}, {"description", e.Description}} {
			if strings.TrimSpace(f.value) == "" {
				bad("%s needs a %s", e.ID, f.name)
			}
		}
		if len(e.Versions) == 0 {
			bad("%s has no versions", e.ID)
		}
		owner, _, _ := strings.Cut(e.ID, "/")
		versions := map[string]bool{}
		for _, v := range e.Versions {
			where := e.ID + " " + v.Version
			if strings.HasPrefix(v.Version, "v") || !semver.IsValid("v"+v.Version) {
				bad("%s: the version must look like 1.2.3 (no leading v)", where)
			}
			if versions[v.Version] {
				bad("%s is listed twice", where)
			}
			versions[v.Version] = true
			if !apiPattern.MatchString(v.API) {
				bad("%s: api must be the contract the plugin was built for, such as girvi/v1", where)
			}
			if !sha256Pattern.MatchString(v.Bundle.SHA256) {
				bad("%s: the sha256 must be 64 lowercase hex characters", where)
			}
			if v.Bundle.Size <= 0 || v.Bundle.Size > MaxBundleSize {
				bad("%s: the size must be between 1 byte and %d MB", where, MaxBundleSize>>20)
			}
			if msg := checkBundleURL(v.Bundle.URL, owner, p); msg != "" {
				bad("%s: %s", where, msg)
			}
		}
	}
	return problems
}

func checkBundleURL(raw, owner string, p Policy) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "the bundle must be an https address"
	}
	if p.OwnerMustMatchID {
		allowed := []string{owner}
		if accounts, reserved := reservedOwners[owner]; reserved {
			allowed = accounts
		}
		if !isReleaseAssetOf(u, allowed) {
			return fmt.Sprintf("the bundle must be a GitHub release asset (github.com/<owner>/<repo>/releases/download/<tag>/<file>) of github.com/%s, the account allowed for the plugin id", strings.Join(allowed, " or github.com/"))
		}
	}
	return ""
}

// isReleaseAssetOf checks the exact shape of a GitHub release download, so the
// account in it is the account the file really comes from. A prefix check is
// not enough: dot segments ("..", "." and their percent-encoded forms) would
// let a path that starts with an allowed account walk into any other
// repository.
func isReleaseAssetOf(u *url.URL, owners []string) bool {
	if u.Host != "github.com" || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	// <owner>/<repo>/releases/download/<tag, which may hold a slash>/<file>
	if len(segments) < 6 || segments[2] != "releases" || segments[3] != "download" {
		return false
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `%\`) {
			return false
		}
	}
	return slices.Contains(owners, segments[0])
}

// reservedOwners are ids only the named GitHub accounts may publish. "girvi/..."
// is the official namespace, so nobody else can list a plugin as if it were one.
var reservedOwners = map[string][]string{"girvi": {"anant-barlota"}}

// CheckImmutable refuses a change to a version that was already published,
// which would swap a bundle people may have installed. Adding versions and
// removing a bad one are allowed.
func CheckImmutable(old, cur *Index) []string {
	var problems []string
	have := map[string]Version{}
	for _, e := range cur.Plugins {
		for _, v := range e.Versions {
			have[e.ID+"@"+v.Version] = v
		}
	}
	for _, e := range old.Plugins {
		for _, v := range e.Versions {
			now, ok := have[e.ID+"@"+v.Version]
			if !ok {
				continue
			}
			a, _ := json.Marshal(v)
			b, _ := json.Marshal(now)
			if !bytes.Equal(a, b) {
				problems = append(problems, fmt.Sprintf("%s %s was published before and cannot be changed. Release a new version instead", e.ID, v.Version))
			}
		}
	}
	return problems
}

// Canonical orders the catalog the same way every time, so a change shows up
// as a small diff.
func Canonical(idx *Index) []byte {
	sort.Slice(idx.Plugins, func(i, j int) bool { return idx.Plugins[i].ID < idx.Plugins[j].ID })
	for i := range idx.Plugins {
		vs := idx.Plugins[i].Versions
		sort.Slice(vs, func(a, b int) bool { return semver.Compare("v"+vs[a].Version, "v"+vs[b].Version) < 0 })
	}
	out, _ := json.MarshalIndent(idx, "", "  ")
	return append(out, '\n')
}
