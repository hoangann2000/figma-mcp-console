// figma-mcp is a stdio MCP server that lets AI clients read and write Figma
// documents through a local WebSocket bridge to a Figma plugin.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
	"github.com/hoangann2000/figma-mcp-console/internal/tools"
)

const defaultPort = 2000

func main() {
	// stdout is the MCP JSON-RPC transport; all logging must go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("figma-mcp: ")

	port := flag.Int("port", 0, "WebSocket bridge port (default $FIGMA_MCP_PORT or 2000)")
	flag.Parse()
	if *port == 0 {
		if env := os.Getenv("FIGMA_MCP_PORT"); env != "" {
			fmt.Sscanf(env, "%d", port)
		}
	}
	if *port == 0 {
		*port = defaultPort
	}

	b := bridge.New()
	go func() {
		if err := b.Serve(fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
			log.Fatalf("bridge failed: %v", err)
		}
	}()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "figma-console",
		Title:   "Figma MCP Console",
		Version: "0.1.0",
	}, nil)
	tools.Register(server, b)
	tools.RegisterPrompts(server)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp server stopped: %v", err)
	}
}
