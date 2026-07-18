package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPrompts adds the built-in workflow prompts to the MCP server.
func RegisterPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "design_workflow",
		Description: "Step-by-step workflow for creating a design in Figma from a brief.",
		Arguments: []*mcp.PromptArgument{
			{Name: "brief", Description: "What to design (e.g. 'a login screen for a fintech app')", Required: true},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		brief := req.Params.Arguments["brief"]
		text := fmt.Sprintf(`Design the following in Figma: %s

Follow this workflow, using the figma-console MCP tools:

1. Inspect first: call get_metadata and get_selection to understand the current file and where to place new work. In large files, locate existing layers with find_nodes instead of walking the tree.
2. Create a root frame with create_frame at an empty spot on the page, sized for the target device (e.g. 390x844 for mobile, 1440x900 for desktop). All other layers go inside it via parent_id.
3. Build the layout top-down with create_frame / create_rectangle / create_text, giving every node a descriptive name. Prefer set_auto_layout on container frames (direction, item_spacing, padding) over manual x/y positioning — only position absolutely when auto-layout does not fit.
4. Style: set_fills for fill colors, set_corner_radius for rounded corners, set_strokes for borders, set_effects for shadows and blurs.
5. Verify visually: call get_screenshot on the root frame after each meaningful chunk of work and compare against the brief.
6. Iterate: fix issues with move_nodes, resize_nodes, set_characters, set_fills, remove_nodes until the screenshot matches the intent.
7. Finish by calling set_selection on the root frame so the user can see the result. If the user wants assets in their codebase, export icons/images with download_assets (SVG for icons).`, brief)
		return &mcp.GetPromptResult{
			Description: "Figma design workflow",
			Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: text}},
			},
		}, nil
	})
}
