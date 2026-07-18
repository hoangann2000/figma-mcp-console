package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

// TestEndToEnd runs the real server binary (via `go run`), attaches a fake
// Figma plugin over the WebSocket bridge, and drives it with the real MCP
// client — the full production message path minus Figma itself.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Free port for the bridge so parallel test runs don't collide.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Run the server from a temp dir so download_assets writes there, not
	// into the package directory. `go run` can't change its working directory
	// without losing the module context, so build the binary first.
	projectDir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "figma-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Dir = projectDir
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("FIGMA_MCP_PORT=%d", port))

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if n := len(tools.Tools); n != 43 {
		t.Errorf("want 43 tools, got %d", n)
	}

	// Before the plugin connects: friendly error, not a timeout.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_metadata"})
	if err != nil {
		t.Fatalf("call get_metadata: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "not connected") {
		t.Errorf("want not-connected tool error, got %+v", res)
	}

	// Attach a fake plugin that answers get_metadata.
	var conn *websocket.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, _, err = websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws", port), nil)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("fake plugin could not dial bridge: %v", err)
	}
	defer conn.CloseNow()
	go func() {
		for {
			var req bridge.Request
			if err := wsjson.Read(ctx, conn, &req); err != nil {
				return
			}
			var result []byte
			if req.Command == "download_assets" {
				result, _ = json.Marshal([]map[string]any{{
					"data":   base64.StdEncoding.EncodeToString([]byte("<svg/>")),
					"format": "SVG", "name": "icon", "width": 24, "height": 24,
				}})
			} else {
				result, _ = json.Marshal(map[string]any{"file": "Fake File", "command": req.Command})
			}
			wsjson.Write(ctx, conn, bridge.Response{ID: req.ID, Result: result})
		}
	}()

	// The bridge adopts connections asynchronously; retry until it answers.
	deadline = time.Now().Add(5 * time.Second)
	for {
		res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_metadata"})
		if err != nil {
			t.Fatalf("call get_metadata with plugin: %v", err)
		}
		if !res.IsError || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if res.IsError {
		t.Fatalf("get_metadata still failing with plugin attached: %+v", res.Content)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Fake File") {
		t.Errorf("unexpected get_metadata result: %s", text)
	}

	// download_assets writes the decoded asset inside the project directory.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "download_assets",
		Arguments: map[string]any{"items": []map[string]any{
			{"node_id": "1:2", "path": "assets/icon.svg"},
		}},
	})
	if err != nil {
		t.Fatalf("call download_assets: %v", err)
	}
	if res.IsError {
		t.Fatalf("download_assets failed: %+v", res.Content)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "assets", "icon.svg"))
	if err != nil {
		t.Fatalf("exported file not written: %v", err)
	}
	if string(data) != "<svg/>" {
		t.Errorf("exported file content = %q, want <svg/>", data)
	}

	// Paths escaping the project directory are rejected before touching disk.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "download_assets",
		Arguments: map[string]any{"items": []map[string]any{
			{"node_id": "1:2", "path": "../evil.svg"},
		}},
	})
	if err != nil {
		t.Fatalf("call download_assets escape: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "escapes") {
		t.Errorf("want path-escape error, got %+v", res.Content)
	}
}
