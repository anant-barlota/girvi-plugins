package tool

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anant-barlota/girvi-sdk/plugin/registry"
)

// PublishRequest lists a released plugin in a catalog.
type PublishRequest struct {
	Dir      string `json:"dir" jsonschema:"the plugin folder; its plugin.json names the version to list"`
	Repo     string `json:"repo" jsonschema:"GitHub repository whose release holds the bundle, owner/repo"`
	Tag      string `json:"tag,omitempty" jsonschema:"the release tag; defaults to v<version>"`
	Index    string `json:"index" jsonschema:"path to index.json in a checkout of the catalog repository"`
	Homepage string `json:"homepage,omitempty" jsonschema:"the plugin's homepage; defaults to https://github.com/<repo> for a new plugin"`
	License  string `json:"license,omitempty" jsonschema:"the plugin's license, such as MIT"`
}

// Published is what Publish added to the catalog.
type Published struct {
	Index   string          `json:"index"`
	ID      string          `json:"id"`
	Version string          `json:"version"`
	Bundle  registry.Bundle `json:"bundle"`
	Next    []string        `json:"next"`
}

// Publish downloads the bundle released for the version in dir's plugin.json,
// and adds it to the catalog at req.Index with its checksum, size and grants.
// It refuses a release that is not this plugin.json's id and version, and a
// catalog the result would make invalid.
func Publish(ctx context.Context, c *http.Client, req PublishRequest) (*Published, error) {
	if req.Repo == "" || req.Index == "" {
		return nil, errors.New("say which repository has the release (owner/repo) and where the catalog's index.json is")
	}
	m, _, err := readManifest(req.Dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Author) == "" || strings.TrimSpace(m.Description) == "" {
		return nil, errors.New("plugin.json needs an author and a description: the catalog shows both")
	}
	tag := req.Tag
	if tag == "" {
		tag = "v" + m.Version
	}
	_, short, _ := strings.Cut(m.ID, "/")
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s-%s.zip", req.Repo, tag, short, m.Version)
	data, err := registry.Download(ctx, c, url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w. Release this version first", url, err)
	}
	id, v, err := registry.FromBundle(url, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if id != m.ID || v.Version != m.Version {
		return nil, fmt.Errorf("the released bundle is %s %s, but plugin.json says %s %s", id, v.Version, m.ID, m.Version)
	}

	raw, err := os.ReadFile(req.Index)
	if err != nil {
		return nil, err
	}
	idx, err := registry.Parse(raw)
	if err != nil {
		return nil, err
	}
	homepage := req.Homepage
	if homepage == "" {
		homepage = "https://github.com/" + req.Repo
	}
	e := registry.Entry{ID: m.ID, Name: m.Name, Author: m.Author, Description: m.Description, Homepage: homepage, License: req.License}
	if err := registry.Add(idx, e, v); err != nil {
		return nil, err
	}
	if problems := registry.Validate(idx, registry.Policy{OwnerMustMatchID: true}); len(problems) > 0 {
		return nil, fmt.Errorf("the catalog would not be valid:\n- %s", strings.Join(problems, "\n- "))
	}
	if err := os.WriteFile(req.Index, registry.Canonical(idx), 0o644); err != nil {
		return nil, err
	}
	return &Published{Index: req.Index, ID: m.ID, Version: m.Version, Bundle: v.Bundle, Next: []string{
		"Commit index.json and open a pull request to the catalog. Its check downloads the bundle again and compares it.",
	}}, nil
}
