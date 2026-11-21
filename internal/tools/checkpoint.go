package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

// A checkpoint is the design of one frame as the user saved it from the
// plugin window ("Save checkpoint"). It lives outside any project, keyed by
// Figma file and node, so the plugin buttons and diff_design share it
// whichever directory the AI client was started in.

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// checkpointDir can be redirected by tests.
var checkpointDir = func() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "figma-mcp-console", "checkpoints"), nil
}

func checkpointPath(file bridge.FileInfo, nodeID string) (string, error) {
	root, err := checkpointDir()
	if err != nil {
		return "", err
	}
	f := file.Key
	if f == "" {
		f = file.Name // unsaved drafts have no key
	}
	f, n := unsafeName.ReplaceAllString(f, "-"), unsafeName.ReplaceAllString(nodeID, "-")
	if strings.Trim(f, "-") == "" || strings.Trim(n, "-") == "" {
		return "", fmt.Errorf("checkpoint needs a file and a node id")
	}
	return filepath.Join(root, f, n+".json.gz"), nil
}

// writeSnapshot saves a design tree, gzip-compressed when the path ends in .gz.
func writeSnapshot(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if strings.HasSuffix(dest, ".gz") {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	return os.WriteFile(dest, data, 0o644)
}

// readSnapshot loads a design tree; gzip is detected by its magic bytes, so
// a renamed snapshot still loads. A missing file is os.IsNotExist.
func readSnapshot(dest string) ([]byte, error) {
	old, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	if len(old) > 2 && old[0] == 0x1f && old[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(old))
		if err != nil {
			return nil, fmt.Errorf("read baseline: %w", err)
		}
		if old, err = io.ReadAll(zr); err != nil {
			return nil, fmt.Errorf("read baseline: %w", err)
		}
	}
	return old, nil
}

type checkpointRequest struct {
	Op     string `json:"op"` // status, save or diff
	NodeID string `json:"node_id"`
}

type checkpointResult struct {
	Op      string         `json:"op"`
	NodeID  string         `json:"node_id"`
	Saved   string         `json:"saved,omitempty"` // when the checkpoint was saved; empty if none
	Total   int            `json:"total"`           // changes found, before the list was capped
	Changes []designChange `json:"changes,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// maxPanelChanges keeps the plugin window's list readable.
const maxPanelChanges = 200

// checkpointHook answers the plugin window's checkpoint buttons.
func checkpointHook(ctx context.Context, file bridge.FileInfo, raw json.RawMessage,
	call func(command string, params any) (json.RawMessage, error)) any {
	var req checkpointRequest
	_ = json.Unmarshal(raw, &req)
	res := checkpointResult{Op: req.Op, NodeID: req.NodeID}
	fail := func(err error) any { res.Error = err.Error(); return res }
	dest, err := checkpointPath(file, req.NodeID)
	if err != nil {
		return fail(err)
	}
	stamp := func() {
		if info, err := os.Stat(dest); err == nil {
			res.Saved = info.ModTime().Format("2006-01-02 15:04")
		}
	}
	if req.Op == "status" {
		stamp()
		return res
	}
	// Depth 100 reaches every layer of any real screen.
	cur, err := call("get_design_context", map[string]any{"node_id": req.NodeID, "depth": 100})
	if err != nil {
		return fail(err)
	}
	switch req.Op {
	case "save":
		if err := writeSnapshot(dest, cur); err != nil {
			return fail(err)
		}
	case "diff":
		old, err := readSnapshot(dest)
		if os.IsNotExist(err) {
			return fail(fmt.Errorf("no checkpoint saved for this frame yet"))
		}
		if err != nil {
			return fail(err)
		}
		_, _, changes, err := diffChanges(old, cur)
		if err != nil {
			return fail(err)
		}
		res.Total = len(changes)
		if len(changes) > maxPanelChanges {
			changes = changes[:maxPanelChanges]
		}
		res.Changes = changes
	default:
		return fail(fmt.Errorf("unknown checkpoint op %q", req.Op))
	}
	stamp()
	return res
}
