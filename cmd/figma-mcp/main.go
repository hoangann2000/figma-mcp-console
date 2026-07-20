// figma-mcp is a stdio MCP server that lets AI clients read and write Figma
// documents through a local WebSocket bridge to the Figma plugin.
//
// All sessions on a machine share one bridge on a single port: the first
// session to bind it serves every plugin window (one per open Figma file)
// and every other session; see internal/bridge.Router.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
	"github.com/hoangann2000/figma-mcp-console/internal/tools"
)

// defaultPort must stay in sync with the plugin: both ui.html and the
// manifest's devAllowedDomains point at ws://localhost:2000.
const defaultPort = 2000

func main() {
	// stdout is the MCP JSON-RPC transport; all logging must go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("figma-mcp: ")

	port := flag.Int("port", 0, "WebSocket bridge port (default $FIGMA_MCP_PORT, or 2000)")
	flag.Parse()
	if *port == 0 {
		if env := os.Getenv("FIGMA_MCP_PORT"); env != "" {
			fmt.Sscanf(env, "%d", port)
		}
	}
	if *port == 0 {
		*port = defaultPort
	}

	// Exit when the MCP client that spawned us dies. An orphaned session
	// would otherwise linger in the bridge election forever.
	go func() {
		ppid := os.Getppid()
		for range time.Tick(5 * time.Second) {
			if os.Getppid() != ppid {
				log.Print("mcp client exited, shutting down")
				os.Exit(0)
			}
		}
	}()

	project := ""
	if cwd, err := os.Getwd(); err == nil {
		project = filepath.Base(cwd)
	}
	r := bridge.NewRouter(project, *port)
	go r.Run()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "figma-console",
		Title:   "Figma MCP Console",
		Version: "0.5.0",
	}, nil)
	tools.Register(server, r)
	tools.RegisterPrompts(server)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp server stopped: %v", err)
	}
}
