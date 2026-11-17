package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin/manifest"
)

// FromBundle reads a bundle published at url: the plugin id, and the catalog
// version for it, with what its plugin.json asks for and the bundle's checksum
// and size.
func FromBundle(url string, zip []byte) (id string, v Version, err error) {
	b, err := manifest.ReadBundle(zip)
	if err != nil {
		return "", Version{}, err
	}
	m := b.Manifest
	return m.ID, Version{
		Version:   m.Version,
		API:       m.API,
		Events:    m.Events,
		Fields:    m.Fields,
		HTTPHosts: m.HTTPHosts,
		Bundle:    Bundle{URL: url, SHA256: b.Hash, Size: int64(len(zip))},
	}, nil
}

// Download fetches a bundle, refusing anything larger than MaxBundleSize.
func Download(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBundleSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBundleSize {
		return nil, fmt.Errorf("larger than %d MB", MaxBundleSize>>20)
	}
	return data, nil
}

// VerifyBundles downloads every listed bundle and checks it is the one the
// catalog describes: same checksum and size, a plugin.json Girvi accepts, and
// the same id, version and grants as the catalog shows before install.
func VerifyBundles(ctx context.Context, c *http.Client, idx *Index) []string {
	var problems []string
	for _, e := range idx.Plugins {
		for _, v := range e.Versions {
			where := e.ID + " " + v.Version
			bad := func(format string, a ...any) {
				problems = append(problems, where+": "+fmt.Sprintf(format, a...))
			}
			data, err := Download(ctx, c, v.Bundle.URL)
			if err != nil {
				bad("could not download the bundle: %v", err)
				continue
			}
			id, got, err := FromBundle(v.Bundle.URL, data)
			if err != nil {
				bad("%v", err)
				continue
			}
			if got.Bundle.SHA256 != v.Bundle.SHA256 {
				bad("the bundle's sha256 is %s, not %s", got.Bundle.SHA256, v.Bundle.SHA256)
			}
			if got.Bundle.Size != v.Bundle.Size {
				bad("the bundle's size is %d, not %d", got.Bundle.Size, v.Bundle.Size)
			}
			if id != e.ID {
				bad("the bundle's plugin.json says id %q", id)
			}
			if got.Version != v.Version {
				bad("the bundle's plugin.json says version %q", got.Version)
			}
			if got.API != v.API {
				bad("the bundle's plugin.json says api %q", got.API)
			}
			for _, f := range []struct {
				name      string
				want, has []string
			}{{"events", got.Events, v.Events}, {"fields", got.Fields, v.Fields}, {"http_hosts", got.HTTPHosts, v.HTTPHosts}} {
				if !slices.Equal(f.want, f.has) {
					bad("%s are %v in the bundle's plugin.json but %v here", f.name, f.want, f.has)
				}
			}
		}
	}
	return problems
}
