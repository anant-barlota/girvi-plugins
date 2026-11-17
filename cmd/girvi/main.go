// Command girvi is the Girvi command line. Today it makes, checks, builds and
// tests plugins, and serves the same as an MCP server for AI assistants.
//
//	girvi plugin new <dir> --id publisher/name [--name N] [--description D] [--sdk PATH]
//	girvi plugin validate [dir]
//	girvi plugin build [dir]
//	girvi plugin test [dir] [--event TYPE] [--kind event|test|wake] [--wake PAYLOAD]
//	                  [--set key=value]... [--respond STATUS[:BODY]]... [--json]
//	girvi contract [--schema | --event TYPE]
//	girvi mcp
//	girvi version
//
// Install it with: go install github.com/anant-barlota/girvi-plugins/cmd/girvi@latest
//
// It never contains the Girvi app. Commands that act on a shop are to reach a
// running Girvi through its API, as gh reaches GitHub.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/harness"
	"github.com/anant-barlota/girvi-plugins/internal/mcpserver"
	"github.com/anant-barlota/girvi-plugins/internal/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `girvi is the Girvi command line.

  girvi plugin    make, check, build and test plugins
  girvi contract  print the girvi/v1 plugin contract
  girvi mcp       serve the plugin commands to an AI assistant (MCP, over stdin and stdout)
  girvi version   the runtime version this tool runs plugins with

Run a command with -h for its options.
`

const pluginUsage = `girvi plugin makes, checks, builds and tests Girvi plugins.

  girvi plugin new <dir> --id publisher/name   make a plugin that builds and passes its tests
  girvi plugin validate [dir]                  check plugin.json, and the build, with Girvi's rules
  girvi plugin build [dir]                     compile to dist/plugin.wasm and zip the bundle
  girvi plugin test [dir]                      run the built plugin against a sample event

Run a command with -h for its options. dir defaults to the current folder.
`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch args[0] {
	case "plugin":
		return runPlugin(ctx, args[1:])
	case "contract":
		return cmdContract(os.Stdout, args[1:])
	case "mcp":
		return mcpserver.New().Run(ctx, &mcp.StdioTransport{})
	case "version":
		fmt.Println(runtimeVersion())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
	return nil
}

func runPlugin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, pluginUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "new":
		return cmdNew(ctx, args[1:])
	case "validate":
		return cmdValidate(ctx, args[1:])
	case "build":
		return cmdBuild(ctx, args[1:])
	case "test":
		return cmdTest(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(pluginUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", "plugin "+args[0], pluginUsage)
		os.Exit(2)
	}
	return nil
}

// parse lets flags come before or after the one positional argument.
func parse(fs *flag.FlagSet, args []string) (string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	switch len(pos) {
	case 0:
		return ".", nil
	case 1:
		return pos[0], nil
	}
	return "", fmt.Errorf("expected one folder, got %s", strings.Join(pos, " "))
}

func cmdNew(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin new", flag.ExitOnError)
	var req tool.NewRequest
	fs.StringVar(&req.ID, "id", "", "plugin id, publisher/name (required)")
	fs.StringVar(&req.Name, "name", "", "name shown to the user")
	fs.StringVar(&req.Description, "description", "", "one sentence shown to the user")
	fs.StringVar(&req.SDK, "sdk", "", "a local girvi-plugins checkout to use instead of a released SDK")
	dir, err := parse(fs, args)
	if err != nil {
		return err
	}
	if dir == "." {
		return errors.New("say which folder to make: girvi plugin new <dir> --id publisher/name")
	}
	req.Dir = dir
	res, err := tool.New(ctx, req)
	if err != nil {
		return err
	}
	fmt.Printf("Made %s: %s\n", res.Dir, strings.Join(res.Files, ", "))
	if res.Tidy != "" {
		fmt.Printf("\ngo mod tidy failed; run it again in %s once the SDK can be fetched:\n%s\n", res.Dir, res.Tidy)
	}
	fmt.Println("\nNext:")
	for _, n := range res.Next {
		fmt.Println("  " + n)
	}
	return nil
}

func cmdValidate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin validate", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	dir, err := parse(fs, args)
	if err != nil {
		return err
	}
	r := tool.Validate(ctx, dir)
	if *asJSON {
		return printJSON(r)
	}
	for _, p := range r.Problems {
		fmt.Println("problem:", p)
	}
	for _, n := range r.Notes {
		fmt.Println("note:", n)
	}
	if !r.OK {
		return errors.New("not valid")
	}
	if r.Built {
		fmt.Printf("valid: plugin.json and dist/plugin.wasm (exports %s)\n", strings.Join(r.Exports, ", "))
	} else {
		fmt.Println("valid: plugin.json")
	}
	return nil
}

func cmdBuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin build", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	dir, err := parse(fs, args)
	if err != nil {
		return err
	}
	b, err := tool.Build(ctx, dir)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(b)
	}
	fmt.Printf("built %s (%.1f MB)\nsha256 %s\nexports %s\n", b.Bundle, float64(b.Size)/(1<<20), b.SHA256, strings.Join(b.Exports, ", "))
	return nil
}

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func cmdTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin test", flag.ExitOnError)
	var req tool.TestRequest
	var sets, responds repeated
	fs.StringVar(&req.Event, "event", "", "sample event to deliver: "+strings.Join(contract.Events(), ", "))
	fs.StringVar(&req.Kind, "kind", "", "event (default), test, or wake")
	fs.StringVar(&req.Wake, "wake", "", "payload for --kind wake")
	fs.Var(&sets, "set", "a setting, key=value (repeat for more)")
	fs.Var(&responds, "respond", "answer to the next HTTP request, STATUS or STATUS:BODY, or error:TEXT (repeat for more)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	dir, err := parse(fs, args)
	if err != nil {
		return err
	}
	req.Dir = dir
	req.Settings = map[string]string{}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("--set %q: use key=value", s)
		}
		req.Settings[k] = v
	}
	for _, r := range responds {
		if text, ok := strings.CutPrefix(r, "error:"); ok {
			req.Responses = append(req.Responses, tool.TestResponse{Error: text})
			continue
		}
		code, body, _ := strings.Cut(r, ":")
		status, err := strconv.Atoi(code)
		if err != nil {
			return fmt.Errorf("--respond %q: use STATUS, STATUS:BODY or error:TEXT", r)
		}
		req.Responses = append(req.Responses, tool.TestResponse{Status: status, Body: body})
	}
	res, err := tool.Test(ctx, req)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	printResult(os.Stdout, res)
	return nil
}

// runtimeVersion names the sandbox this tool runs plugins with. Girvi shows
// its own on the Plugins screen; the same version runs a plugin the same way.
func runtimeVersion() string {
	if v := contract.Version(); v != "" {
		return "girvi-plugins " + v
	}
	return "girvi-plugins (built from a checkout)"
}

func printResult(w io.Writer, r *harness.Result) {
	fmt.Fprintln(w, "runtime:", runtimeVersion())
	fmt.Fprintf(w, "%s, %s %s (%s)\n", r.Export, r.Input.Kind, r.Input.EventType(), r.Duration.Round(1e6))
	if r.Outcome != nil {
		fmt.Fprintf(w, "outcome: %s", r.Outcome.Kind)
		if r.Outcome.Kind == "retry" {
			fmt.Fprintf(w, " in %ds", r.Outcome.RetryInSeconds)
		}
		if r.Outcome.Note != "" {
			fmt.Fprintf(w, ": %s", r.Outcome.Note)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintf(w, "no answer: %s\n", r.NoAnswer)
	}
	for _, l := range r.Logs {
		fmt.Fprintln(w, "log:", l)
	}
	for _, q := range r.Requests {
		line := q.Method + " " + q.URL
		if q.Refused != "" {
			line += "  REFUSED: " + q.Refused
		}
		fmt.Fprintln(w, "request:", line)
	}
	for k, v := range r.Storage {
		fmt.Fprintf(w, "storage: %s = %q\n", k, v)
	}
	for _, wk := range r.Wakes {
		if wk.Refused {
			fmt.Fprintf(w, "wake: refused (%q)\n", wk.Payload)
		} else {
			fmt.Fprintf(w, "wake: at %s (%q)\n", wk.At.Format("2006-01-02 15:04:05Z07:00"), wk.Payload)
		}
	}
}

func cmdContract(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("contract", flag.ExitOnError)
	schema := fs.Bool("schema", false, "print the JSON schema of plugin.json")
	event := fs.String("event", "", "print a sample event: "+strings.Join(contract.Events(), ", "))
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *schema:
		_, err := w.Write(contract.Schema())
		return err
	case *event != "":
		raw, err := contract.Fixture(*event)
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	}
	_, err := io.WriteString(w, contract.Text())
	return err
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
