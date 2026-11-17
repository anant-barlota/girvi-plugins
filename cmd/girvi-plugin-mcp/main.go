// Command girvi-plugin-mcp is an MCP server, over stdin and stdout, that lets
// an AI assistant write Girvi plugins: read the contract, make a plugin,
// check it, build it and run it against sample events, with no Girvi needed.
//
// Add it to a client (for example Claude Code):
//
//	claude mcp add girvi-plugins -- go run github.com/anant-barlota/girvi-plugins/cmd/girvi-plugin-mcp@latest
//
// Tests send nothing over the network: HTTP requests are answered from the
// script the caller gives, and recorded.
package main

import (
	"context"
	"log"

	"github.com/anant-barlota/girvi-plugins/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := server.New().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
