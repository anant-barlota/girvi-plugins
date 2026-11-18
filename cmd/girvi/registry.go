package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/anant-barlota/girvi-sdk/internal/tool"
	"github.com/anant-barlota/girvi-sdk/plugin/registry"
)

const registryUsage = `girvi registry works with a plugin catalog, the index.json Girvi reads under Plugins, Browse.

  girvi registry check   check the catalog, and download every bundle to confirm it matches

Run a command with -h for its options.
`

func runRegistry(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, registryUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "check":
		return cmdRegistryCheck(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(registryUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", "registry "+args[0], registryUsage)
		os.Exit(2)
	}
	return nil
}

func cmdRegistryCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("registry check", flag.ExitOnError)
	index := fs.String("index", "index.json", "the catalog to check")
	base := fs.String("base", "", "the catalog as it was before this change; published versions may not be altered")
	offline := fs.Bool("offline", false, "check the catalog only, without downloading the bundles")
	anyHost := fs.Bool("any-host", false, "allow bundles that are not GitHub release assets of the id's owner (for a private catalog)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	idx, err := loadIndex(*index)
	if err != nil {
		return err
	}
	problems := registry.Validate(idx, registry.Policy{OwnerMustMatchID: !*anyHost})
	if *base != "" {
		old, err := loadIndex(*base)
		if err != nil {
			return fmt.Errorf("--base: %w", err)
		}
		problems = append(problems, registry.CheckImmutable(old, idx)...)
	}
	if len(problems) == 0 && !*offline {
		problems = registry.VerifyBundles(ctx, http.DefaultClient, idx)
	}
	for _, p := range problems {
		fmt.Println("problem:", p)
	}
	if len(problems) > 0 {
		return errors.New("not valid")
	}
	fmt.Printf("valid: %d plugin(s)\n", len(idx.Plugins))
	return nil
}

func loadIndex(path string) (*registry.Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return registry.Parse(data)
}

func cmdPublish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin publish", flag.ExitOnError)
	var req tool.PublishRequest
	fs.StringVar(&req.Repo, "repo", "", "GitHub repository whose release holds the bundle, owner/repo (required)")
	fs.StringVar(&req.Index, "index", "", "index.json in a checkout of the catalog repository (required)")
	fs.StringVar(&req.Tag, "tag", "", "the release tag (default v<version>)")
	fs.StringVar(&req.Homepage, "homepage", "", "the plugin's homepage (default https://github.com/<repo>)")
	fs.StringVar(&req.License, "license", "", "the plugin's license, such as MIT")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	dir, err := parse(fs, args)
	if err != nil {
		return err
	}
	req.Dir = dir
	p, err := tool.Publish(ctx, http.DefaultClient, req)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(p)
	}
	fmt.Printf("listed %s %s in %s\nbundle %s\nsha256 %s\n\nNext:\n", p.ID, p.Version, p.Index, p.Bundle.URL, p.Bundle.SHA256)
	for _, n := range p.Next {
		fmt.Println("  " + n)
	}
	return nil
}
