package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

// The plugin buttons: no checkpoint, save, nothing changed, then a changed text.
func TestCheckpointHook(t *testing.T) {
	dir := t.TempDir()
	checkpointDir = func() (string, error) { return dir, nil }
	design := `{"id":"1:1","name":"Login","type":"FRAME","children":[{"id":"1:2","name":"Submit","type":"TEXT","text":"Send"}]}`
	call := func(string, any) (json.RawMessage, error) { return json.RawMessage(design), nil }
	file := bridge.FileInfo{Key: "K/../x", Name: "Demo"}
	run := func(op string) checkpointResult {
		raw, _ := json.Marshal(checkpointRequest{Op: op, NodeID: "1:1"})
		return checkpointHook(context.Background(), file, raw, call).(checkpointResult)
	}
	if r := run("status"); r.Saved != "" || r.Error != "" {
		t.Fatalf("fresh frame: %+v", r)
	}
	if r := run("diff"); r.Error == "" {
		t.Fatalf("diff without a checkpoint must say so: %+v", r)
	}
	if r := run("save"); r.Saved == "" || r.Error != "" {
		t.Fatalf("save: %+v", r)
	}
	if r := run("diff"); r.Total != 0 || r.Error != "" {
		t.Fatalf("unchanged design: %+v", r)
	}
	design = `{"id":"1:1","name":"Login","type":"FRAME","children":[{"id":"1:2","name":"Submit","type":"TEXT","text":"Register"}]}`
	r := run("diff")
	if r.Total != 1 || r.Changes[0].Kind != "~" || r.Changes[0].ID != "1:2" {
		t.Fatalf("changed text: %+v", r)
	}
	// The file key is sanitized: nothing is written outside the checkpoint folder.
	p, _ := checkpointPath(file, "1:1")
	if rel := p[len(dir):]; rel != "/K-x/1-1.json.gz" {
		t.Fatalf("path %q", rel)
	}
}
