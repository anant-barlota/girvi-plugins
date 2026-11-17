package mcpserver_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anant-barlota/girvi-plugins/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverEnd, clientEnd := mcp.NewInMemoryTransports()
	ss, err := mcpserver.New().Connect(ctx, serverEnd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientEnd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTools(t *testing.T) {
	ctx := context.Background()
	cs := session(t)
	if init := cs.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "read_contract") {
		t.Fatalf("instructions: %+v", init)
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "build_plugin,new_plugin,read_contract,test_plugin,validate_plugin" {
		t.Fatalf("tools %v", names)
	}

	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_contract", Arguments: map[string]any{}})
	if err != nil || r.IsError || !strings.HasPrefix(text(r), "# The girvi/v1 plugin contract") {
		t.Fatalf("read_contract: %v %+v", err, r)
	}
	r, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_contract", Arguments: map[string]any{"event": "loan.redeemed"}})
	if r.IsError || !strings.Contains(text(r), `"type": "loan.redeemed"`) {
		t.Fatalf("sample event: %s", text(r))
	}

	example, _ := filepath.Abs(filepath.Join("..", "..", "examples", "webhook"))
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "validate_plugin", Arguments: map[string]any{"dir": example}})
	if err != nil || r.IsError || !strings.Contains(text(r), `"ok":true`) {
		t.Fatalf("validate_plugin: %v %s", err, text(r))
	}

	// Make, build and run a plugin through the tools, as an assistant would.
	sdk, _ := filepath.Abs(filepath.Join("..", ".."))
	dir := filepath.Join(t.TempDir(), "mailer")
	for _, step := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"new_plugin", map[string]any{"dir": dir, "id": "your-name/mailer", "sdk": sdk}, `"files"`},
		{"build_plugin", map[string]any{"dir": dir}, `"sha256"`},
		{"test_plugin", map[string]any{"dir": dir}, `"kind":"ok"`},
	} {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: step.tool, Arguments: step.args})
		if err != nil || r.IsError || !strings.Contains(text(r), step.want) {
			t.Fatalf("%s: %v %s", step.tool, err, text(r))
		}
	}

	// A tool failure comes back as a tool error the assistant can read, not a
	// protocol error.
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "test_plugin", Arguments: map[string]any{"dir": t.TempDir()}})
	if err != nil || !r.IsError || !strings.Contains(text(r), "no plugin.json") {
		t.Fatalf("test_plugin on an empty folder: %v %s", err, text(r))
	}
}
