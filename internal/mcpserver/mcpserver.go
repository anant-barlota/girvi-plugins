// Package mcpserver is the MCP server behind girvi mcp. It is a thin
// layer over package tool, so the assistant and the CLI do exactly the same
// things.
package mcpserver

import (
	"context"
	"errors"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/internal/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = `Tools for writing Girvi plugins: sandboxed WebAssembly add-ons for Girvi, a pawn-shop app, that act when a loan or payment is saved.

Start with read_contract: it is the whole contract and is short. Then:
1. new_plugin makes a folder that builds and passes its tests as it is.
2. Edit plugin.json (events, customer fields, http_hosts, settings) and handler.go. Keep logic in handler.go so it runs under plain go test with sdk/sdktest.
3. build_plugin compiles to WebAssembly, checks it the way Girvi does at install, and writes dist/<name>-<version>.zip.
4. test_plugin runs the built plugin against a sample event and returns its outcome, logs, HTTP requests (scripted; nothing is sent), storage and wake-ups.
validate_plugin checks plugin.json and the build at any time.

The plugin decides each outcome: ok, failed (never works), blocked (a setting needs fixing), or retry (with a delay). Money is integer paise with a display string; never compute or format it.`

// New returns the server with every tool.
func New() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "girvi-plugins", Version: version()}, &mcp.ServerOptions{Instructions: instructions})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_contract",
		Description: "The girvi/v1 plugin contract: plugin.json, exports, host functions, outcomes, limits, deliveries and events. Optionally the JSON schema of plugin.json, or a sample event.",
	}, readContract)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "new_plugin",
		Description: "Make a new plugin folder (plugin.json, handler.go, a test, main.go with the exports, go.mod) that builds and passes its tests as it is.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tool.NewRequest) (*mcp.CallToolResult, *tool.NewResult, error) {
		res, err := tool.New(ctx, in)
		return nil, res, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "validate_plugin",
		Description: "Check a plugin folder's plugin.json, and dist/plugin.wasm if it is built, with the rules Girvi applies at install.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dirInput) (*mcp.CallToolResult, tool.Report, error) {
		return nil, tool.Validate(ctx, in.Dir), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "build_plugin",
		Description: "Compile a plugin folder to dist/plugin.wasm, check it as Girvi would at install, and zip the bundle to dist/<name>-<version>.zip. Returns the bundle's SHA-256, which is what a user approves.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dirInput) (*mcp.CallToolResult, *tool.Built, error) {
		b, err := tool.Build(ctx, in.Dir)
		return nil, b, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "test_plugin",
		Description: "Run a built plugin once, as Girvi would, against a sample event (or a wake-up). Returns its outcome, or why it gave none, and its logs, HTTP requests (answered from the script; nothing is sent), storage and wake-ups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tool.TestRequest) (*mcp.CallToolResult, any, error) {
		// The result carries the event as raw JSON, which a generated output
		// schema would describe as bytes; it is returned without a schema.
		res, err := tool.Test(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		return nil, res, nil
	})
	return s
}

type dirInput struct {
	Dir string `json:"dir" jsonschema:"the plugin folder"`
}

type contractInput struct {
	Schema bool   `json:"schema,omitempty" jsonschema:"return the JSON schema of plugin.json instead"`
	Event  string `json:"event,omitempty" jsonschema:"return a sample event of this type instead: loan.created, payment.received or loan.redeemed"`
}

type contractOutput struct {
	Text   string   `json:"text"`
	Events []string `json:"events"`
}

func readContract(_ context.Context, _ *mcp.CallToolRequest, in contractInput) (*mcp.CallToolResult, contractOutput, error) {
	out := contractOutput{Events: contract.Events()}
	switch {
	case in.Schema && in.Event != "":
		return nil, out, errors.New("ask for the schema or an event, not both")
	case in.Schema:
		out.Text = string(contract.Schema())
	case in.Event != "":
		raw, err := contract.Fixture(in.Event)
		if err != nil {
			return nil, out, err
		}
		out.Text = string(raw)
	default:
		out.Text = contract.Text()
	}
	// The text is what an assistant reads; send it as plain text, not as a
	// JSON string full of escapes.
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Text}}}, out, nil
}

func version() string {
	if v := tool.SDKVersion(); v != "" {
		return v
	}
	return "dev"
}
