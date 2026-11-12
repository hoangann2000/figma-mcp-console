package tools

import (
	"os"
	"regexp"
	"testing"
)

// Every bridged tool is dispatched to a plugin handler of the same name, so a
// tool registered without its handler (or renamed on one side only) fails at
// runtime with "unknown command". Catch that here instead.
func TestEveryBridgedToolHasAPluginHandler(t *testing.T) {
	goSrc, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatal(err)
	}
	pluginSrc, err := os.ReadFile("../../plugin/code.js")
	if err != nil {
		t.Fatal(err)
	}

	handlers := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{2}async (\w+)\(`).FindAllSubmatch(pluginSrc, -1) {
		handlers[string(m[1])] = true
	}
	if len(handlers) == 0 {
		t.Fatal("found no handlers in plugin/code.js — the handler regex is stale")
	}

	// Both registerBridged[...](s, b, "name" and the hand-written
	// mcp.AddTool(s, &mcp.Tool{Name: "name" forms reach a plugin command.
	tools := regexp.MustCompile(`registerBridged\[\w+\]\(s, b, "(\w+)"`).FindAllSubmatch(goSrc, -1)
	if len(tools) == 0 {
		t.Fatal("found no registered tools in tools.go — the tool regex is stale")
	}
	for _, m := range tools {
		if name := string(m[1]); !handlers[name] {
			t.Errorf("tool %q has no matching handler in plugin/code.js", name)
		}
	}
}
