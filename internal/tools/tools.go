// Package tools registers the MCP tools that proxy commands to the Figma
// plugin over the bridge.
package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

const screenshotTimeout = 60 * time.Second

// downloadTimeout grows with batch size: exports run per node in the
// plugin, so a flat cap would starve large batches. Never below the
// screenshot timeout, capped at 5 minutes.
func downloadTimeout(items int) time.Duration {
	d := screenshotTimeout + time.Duration(items)*5*time.Second
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}

// fileArg routes a call when several Figma files run the plugin at once.
// Embed it in every tool's args struct.
type fileArg struct {
	File string `json:"file,omitempty" jsonschema:"which connected Figma file to act on: its file name (case-insensitive, unique substring ok) or file key, see list_files. Omit when only one file is connected"`
}

func (f fileArg) fileTarget() string { return f.File }

type fileTargeted interface{ fileTarget() string }

// registerBridged wires an MCP tool named name straight to the plugin
// command of the same name: typed args in, raw JSON result out as text.
func registerBridged[In fileTargeted](s *mcp.Server, b *bridge.Router, name, desc string, timeout time.Duration) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, req *mcp.CallToolRequest, args In) (*mcp.CallToolResult, any, error) {
			raw, err := b.Call(ctx, args.fileTarget(), name, args, timeout)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
			}, nil, nil
		})
}

type emptyArgs struct{ fileArg }

type nodeInfoArgs struct {
	fileArg
	NodeID string `json:"node_id" jsonschema:"Figma node ID, e.g. \"12:34\""`
	Depth  int    `json:"depth,omitempty" jsonschema:"how many levels of children to include (default 2)"`
}

type createShapeArgs struct {
	fileArg
	Name      string  `json:"name,omitempty" jsonschema:"layer name"`
	X         float64 `json:"x" jsonschema:"x position in the parent's coordinates"`
	Y         float64 `json:"y" jsonschema:"y position in the parent's coordinates"`
	Width     float64 `json:"width" jsonschema:"width in pixels"`
	Height    float64 `json:"height" jsonschema:"height in pixels"`
	FillColor string  `json:"fill_color,omitempty" jsonschema:"solid fill as hex, e.g. #1E90FF"`
	ParentID  string  `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type createTextArgs struct {
	fileArg
	Text          string   `json:"text" jsonschema:"text content"`
	X             float64  `json:"x" jsonschema:"x position in the parent's coordinates"`
	Y             float64  `json:"y" jsonschema:"y position in the parent's coordinates"`
	FontSize      float64  `json:"font_size,omitempty" jsonschema:"font size in pixels (default 16)"`
	FontFamily    string   `json:"font_family,omitempty" jsonschema:"font family, e.g. Roboto (default Inter); check with list_available_fonts"`
	FontStyle     string   `json:"font_style,omitempty" jsonschema:"font style, e.g. Bold, Medium, Italic (default Regular)"`
	FillColor     string   `json:"fill_color,omitempty" jsonschema:"text color as hex, e.g. #111111"`
	LineHeight    *float64 `json:"line_height,omitempty" jsonschema:"line height in pixels"`
	LetterSpacing *float64 `json:"letter_spacing,omitempty" jsonschema:"letter spacing in pixels (may be negative)"`
	TextAlign     string   `json:"text_align,omitempty" jsonschema:"LEFT, CENTER, RIGHT, or JUSTIFIED"`
	MaxWidth      float64  `json:"max_width,omitempty" jsonschema:"fixed width in pixels; text wraps and grows in height"`
	Name          string   `json:"name,omitempty" jsonschema:"layer name (default: the text content)"`
	ParentID      string   `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type findArgs struct {
	fileArg
	NodeID     string   `json:"node_id,omitempty" jsonschema:"subtree to search in (default: current page)"`
	Name       string   `json:"name,omitempty" jsonschema:"case-insensitive substring to match against layer names"`
	Text       string   `json:"text,omitempty" jsonschema:"case-insensitive substring to match against TEXT node content"`
	Types      []string `json:"types,omitempty" jsonschema:"node types to include, e.g. FRAME, TEXT, COMPONENT, INSTANCE, RECTANGLE"`
	MaxResults int      `json:"max_results,omitempty" jsonschema:"maximum nodes to return (default 50)"`
}

type groupArgs struct {
	fileArg
	NodeIDs []string `json:"node_ids" jsonschema:"nodes to group; the group is created in the first node's parent"`
	Name    string   `json:"name,omitempty" jsonschema:"group name"`
}

type ungroupArgs struct {
	fileArg
	NodeIDs []string `json:"node_ids" jsonschema:"GROUP or FRAME nodes to ungroup, releasing their children to the parent"`
}

type appendChildrenArgs struct {
	fileArg
	ParentID string   `json:"parent_id" jsonschema:"new parent node ID (frame, group, section or page)"`
	NodeIDs  []string `json:"node_ids" jsonschema:"nodes to move into the new parent"`
	Index    *int     `json:"index,omitempty" jsonschema:"insert position among the parent's children (default: append at the end)"`
}

type cloneArgs struct {
	fileArg
	NodeID   string   `json:"node_id" jsonschema:"node to clone"`
	X        *float64 `json:"x,omitempty" jsonschema:"x position for the copy"`
	Y        *float64 `json:"y,omitempty" jsonschema:"y position for the copy"`
	ParentID string   `json:"parent_id,omitempty" jsonschema:"parent to append the copy into (default: same as the original)"`
	Name     string   `json:"name,omitempty" jsonschema:"name for the copy"`
}

type listFontsArgs struct {
	fileArg
	Family      string `json:"family,omitempty" jsonschema:"case-insensitive substring to filter font families"`
	MaxFamilies int    `json:"max_families,omitempty" jsonschema:"maximum families to return (default 50)"`
}

type setTextArgs struct {
	fileArg
	NodeID string `json:"node_id" jsonschema:"ID of a TEXT node"`
	Text   string `json:"text" jsonschema:"new text content"`
}

type gradientStop struct {
	Position float64  `json:"position" jsonschema:"stop position 0..1"`
	Color    string   `json:"color" jsonschema:"stop color as hex, e.g. #FF5733"`
	Opacity  *float64 `json:"opacity,omitempty" jsonschema:"stop alpha 0..1 (default 1)"`
}

type gradientSpec struct {
	Type  string         `json:"type,omitempty" jsonschema:"LINEAR (default) or RADIAL"`
	Stops []gradientStop `json:"stops" jsonschema:"at least 2 color stops"`
	Angle float64        `json:"angle,omitempty" jsonschema:"rotation in degrees for LINEAR (0 = left to right, 90 = top to bottom)"`
}

type setFillArgs struct {
	fileArg
	NodeID   string        `json:"node_id" jsonschema:"target node ID"`
	Color    string        `json:"color,omitempty" jsonschema:"solid fill as hex, e.g. #FF5733 (omit when using gradient)"`
	Gradient *gradientSpec `json:"gradient,omitempty" jsonschema:"gradient fill instead of a solid color"`
	Opacity  *float64      `json:"opacity,omitempty" jsonschema:"fill opacity 0..1 (default 1)"`
}

type createLineArgs struct {
	fileArg
	X            float64  `json:"x" jsonschema:"x position in the parent's coordinates"`
	Y            float64  `json:"y" jsonschema:"y position in the parent's coordinates"`
	Length       float64  `json:"length" jsonschema:"line length in pixels"`
	StrokeColor  string   `json:"stroke_color,omitempty" jsonschema:"line color as hex (default #000000)"`
	StrokeWeight *float64 `json:"stroke_weight,omitempty" jsonschema:"line thickness in pixels (default 1)"`
	Rotation     *float64 `json:"rotation,omitempty" jsonschema:"rotation in degrees (0 = horizontal, 90 = vertical)"`
	Name         string   `json:"name,omitempty" jsonschema:"layer name"`
	ParentID     string   `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type importImageArgs struct {
	fileArg
	Source    string  `json:"source" jsonschema:"image to import: an http(s) URL or a file path relative to the project directory"`
	NodeID    string  `json:"node_id,omitempty" jsonschema:"existing node to apply the image to as a fill; omit to create a new rectangle"`
	X         float64 `json:"x,omitempty" jsonschema:"x position for the new rectangle"`
	Y         float64 `json:"y,omitempty" jsonschema:"y position for the new rectangle"`
	Width     float64 `json:"width,omitempty" jsonschema:"width for the new rectangle (default: the image's natural size)"`
	Height    float64 `json:"height,omitempty" jsonschema:"height for the new rectangle (default: the image's natural size)"`
	ScaleMode string  `json:"scale_mode,omitempty" jsonschema:"FILL (default), FIT, CROP, or TILE"`
	Name      string  `json:"name,omitempty" jsonschema:"layer name"`
	ParentID  string  `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type moveItem struct {
	NodeID string  `json:"node_id" jsonschema:"target node ID"`
	X      float64 `json:"x" jsonschema:"new x position"`
	Y      float64 `json:"y" jsonschema:"new y position"`
}

type moveArgs struct {
	fileArg
	Items []moveItem `json:"items" jsonschema:"nodes to move with their new positions"`
}

type resizeItem struct {
	NodeID string  `json:"node_id" jsonschema:"target node ID"`
	Width  float64 `json:"width" jsonschema:"new width in pixels"`
	Height float64 `json:"height" jsonschema:"new height in pixels"`
}

type resizeArgs struct {
	fileArg
	Items []resizeItem `json:"items" jsonschema:"nodes to resize with their new sizes"`
}

type deleteArgs struct {
	fileArg
	NodeIDs []string `json:"node_ids" jsonschema:"node IDs to delete"`
}

type autoLayoutArgs struct {
	fileArg
	NodeID            string   `json:"node_id" jsonschema:"frame or component node ID"`
	LayoutMode        string   `json:"layout_mode" jsonschema:"HORIZONTAL, VERTICAL, or NONE to remove auto-layout"`
	ItemSpacing       *float64 `json:"item_spacing,omitempty" jsonschema:"gap between children in pixels"`
	Padding           *float64 `json:"padding,omitempty" jsonschema:"uniform padding on all four sides"`
	PaddingTop        *float64 `json:"padding_top,omitempty" jsonschema:"top padding (overrides padding)"`
	PaddingRight      *float64 `json:"padding_right,omitempty" jsonschema:"right padding (overrides padding)"`
	PaddingBottom     *float64 `json:"padding_bottom,omitempty" jsonschema:"bottom padding (overrides padding)"`
	PaddingLeft       *float64 `json:"padding_left,omitempty" jsonschema:"left padding (overrides padding)"`
	PrimaryAxisAlign  string   `json:"primary_axis_align,omitempty" jsonschema:"MIN, CENTER, MAX, or SPACE_BETWEEN"`
	CounterAxisAlign  string   `json:"counter_axis_align,omitempty" jsonschema:"MIN, CENTER, MAX, or BASELINE"`
	PrimaryAxisSizing string   `json:"primary_axis_sizing,omitempty" jsonschema:"FIXED or AUTO (hug contents)"`
	CounterAxisSizing string   `json:"counter_axis_sizing,omitempty" jsonschema:"FIXED or AUTO (hug contents)"`
}

type cornerRadiusArgs struct {
	fileArg
	NodeID      string   `json:"node_id" jsonschema:"target node ID"`
	Radius      *float64 `json:"radius,omitempty" jsonschema:"uniform radius for all corners"`
	TopLeft     *float64 `json:"top_left,omitempty" jsonschema:"top-left corner radius"`
	TopRight    *float64 `json:"top_right,omitempty" jsonschema:"top-right corner radius"`
	BottomRight *float64 `json:"bottom_right,omitempty" jsonschema:"bottom-right corner radius"`
	BottomLeft  *float64 `json:"bottom_left,omitempty" jsonschema:"bottom-left corner radius"`
}

type strokesArgs struct {
	fileArg
	NodeID  string   `json:"node_id" jsonschema:"target node ID"`
	Color   string   `json:"color,omitempty" jsonschema:"solid stroke color as hex, e.g. #333333; omit to remove all strokes"`
	Weight  *float64 `json:"weight,omitempty" jsonschema:"stroke thickness in pixels"`
	Opacity *float64 `json:"opacity,omitempty" jsonschema:"stroke opacity 0..1 (default 1)"`
	Align   string   `json:"align,omitempty" jsonschema:"INSIDE, OUTSIDE, or CENTER"`
}

type effectSpec struct {
	Type         string   `json:"type" jsonschema:"DROP_SHADOW, INNER_SHADOW, LAYER_BLUR, or BACKGROUND_BLUR"`
	Color        string   `json:"color,omitempty" jsonschema:"shadow color as hex (default #000000)"`
	ColorOpacity *float64 `json:"color_opacity,omitempty" jsonschema:"shadow opacity 0..1 (default 0.25)"`
	OffsetX      float64  `json:"offset_x,omitempty" jsonschema:"shadow x offset in pixels"`
	OffsetY      float64  `json:"offset_y,omitempty" jsonschema:"shadow y offset in pixels"`
	Radius       float64  `json:"radius,omitempty" jsonschema:"blur radius in pixels"`
	Spread       float64  `json:"spread,omitempty" jsonschema:"shadow spread in pixels"`
}

type effectsArgs struct {
	fileArg
	NodeID  string       `json:"node_id" jsonschema:"target node ID"`
	Effects []effectSpec `json:"effects" jsonschema:"effects to set, replacing existing ones; empty list removes all effects"`
}

type downloadItem struct {
	NodeID string  `json:"node_id" jsonschema:"node to export"`
	Path   string  `json:"path,omitempty" jsonschema:"destination file path relative to the project directory. Omit it to use the defaults: assets/icons/<layer-name>.svg for SVG, assets/images/<layer-name>.<ext> for PNG/JPG. Set it only when the user or the project's asset conventions specify a destination, e.g. public/icons/arrow-right.svg"`
	Format string  `json:"format,omitempty" jsonschema:"SVG, PNG, or JPG (default: from the path extension, else PNG). SVG only suits pure vector nodes; use PNG when the node has IMAGE fills or blur/shadow effects"`
	Scale  float64 `json:"scale,omitempty" jsonschema:"export scale 0.5..4 for PNG/JPG (default 1)"`
}

type downloadArgs struct {
	fileArg
	Items []downloadItem `json:"items" jsonschema:"assets to export, one file per node"`
}

type exportResult struct {
	Data   string  `json:"data"`
	Format string  `json:"format"`
	Name   string  `json:"name"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Error  string  `json:"error,omitempty"`
}

type setSelectionArgs struct {
	fileArg
	NodeIDs []string `json:"node_ids" jsonschema:"node IDs to select and scroll into view"`
}

type createComponentArgs struct {
	fileArg
	NodeID string `json:"node_id" jsonschema:"FRAME node to convert into a reusable COMPONENT"`
}

type createInstanceArgs struct {
	fileArg
	ComponentID string   `json:"component_id" jsonschema:"COMPONENT node ID to instantiate"`
	X           *float64 `json:"x,omitempty" jsonschema:"x position for the instance"`
	Y           *float64 `json:"y,omitempty" jsonschema:"y position for the instance"`
	ParentID    string   `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type swapComponentArgs struct {
	fileArg
	InstanceID  string `json:"instance_id" jsonschema:"INSTANCE node to swap"`
	ComponentID string `json:"component_id" jsonschema:"COMPONENT to swap the instance to"`
}

type detachInstanceArgs struct {
	fileArg
	InstanceID string `json:"instance_id" jsonschema:"INSTANCE node to detach into a plain frame"`
}

type paintStyleArgs struct {
	fileArg
	Name    string   `json:"name" jsonschema:"style name, e.g. color/primary"`
	Color   string   `json:"color" jsonschema:"solid color as hex, e.g. #1E90FF"`
	Opacity *float64 `json:"opacity,omitempty" jsonschema:"paint opacity 0..1 (default 1)"`
}

type textStyleArgs struct {
	fileArg
	Name       string  `json:"name" jsonschema:"style name, e.g. text/heading-1"`
	FontFamily string  `json:"font_family,omitempty" jsonschema:"font family (default Inter)"`
	FontStyle  string  `json:"font_style,omitempty" jsonschema:"font style, e.g. Bold (default Regular)"`
	FontSize   float64 `json:"font_size,omitempty" jsonschema:"font size in pixels"`
}

type effectStyleArgs struct {
	fileArg
	Name    string       `json:"name" jsonschema:"style name, e.g. effect/card-shadow"`
	Effects []effectSpec `json:"effects" jsonschema:"effects for the style"`
}

type applyStyleArgs struct {
	fileArg
	NodeID  string `json:"node_id" jsonschema:"target node ID"`
	StyleID string `json:"style_id" jsonschema:"style ID from get_local_styles or a create_*_style result"`
	Target  string `json:"target,omitempty" jsonschema:"for PAINT styles: fill (default) or stroke"`
}

type variableCollectionArgs struct {
	fileArg
	Name     string `json:"name" jsonschema:"collection name, e.g. colors"`
	ModeName string `json:"mode_name,omitempty" jsonschema:"rename the default mode, e.g. light"`
}

type addVariableModeArgs struct {
	fileArg
	CollectionID string `json:"collection_id" jsonschema:"variable collection ID"`
	Name         string `json:"name" jsonschema:"new mode name, e.g. dark"`
}

type createVariableArgs struct {
	fileArg
	Name         string `json:"name" jsonschema:"variable name, e.g. color/bg/primary"`
	CollectionID string `json:"collection_id" jsonschema:"variable collection ID"`
	Type         string `json:"type" jsonschema:"COLOR, FLOAT, STRING, or BOOLEAN"`
	Value        any    `json:"value,omitempty" jsonschema:"initial value for the default mode; hex string for COLOR"`
}

type setVariableValueArgs struct {
	fileArg
	VariableID string `json:"variable_id" jsonschema:"variable ID"`
	Value      any    `json:"value" jsonschema:"new value; hex string for COLOR variables"`
	ModeID     string `json:"mode_id,omitempty" jsonschema:"mode to set (default: the collection's default mode)"`
}

type setBoundVariableArgs struct {
	fileArg
	NodeID     string `json:"node_id" jsonschema:"target node ID"`
	Field      string `json:"field" jsonschema:"node field to bind: fills, strokes, width, height, opacity, cornerRadius, itemSpacing, ..."`
	VariableID string `json:"variable_id" jsonschema:"variable to bind to the field"`
}

type screenshotArgs struct {
	fileArg
	NodeID string  `json:"node_id,omitempty" jsonschema:"node to capture (default: current selection, else current page)"`
	Scale  float64 `json:"scale,omitempty" jsonschema:"export scale 0.5..4 (default 1)"`
}

type screenshotResult struct {
	Data   string  `json:"data"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Register adds all Figma tools to the MCP server.
func Register(s *mcp.Server, b *bridge.Router) {
	// list_files is answered by the bridge itself, not a plugin.
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_files",
		Description: "List the Figma files currently connected to the bridge (one per open plugin window). " +
			"When several files are connected, pass one of the returned names as the file parameter of the other tools.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		files, err := b.Files(ctx)
		if err != nil {
			return nil, nil, err
		}
		if len(files) == 0 {
			return nil, nil, bridge.ErrNotConnected
		}
		out, err := json.MarshalIndent(files, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
		}, nil, nil
	})

	registerBridged[emptyArgs](s, b, "get_metadata",
		"Get the current Figma document: file name, current page, and a summary of the page's top-level layers.",
		bridge.DefaultTimeout)
	registerBridged[emptyArgs](s, b, "get_selection",
		"Get the nodes currently selected in Figma, with id, name, type, position and size. "+
			"Each item also carries a shareable figma.com link to that frame/section when the file is saved to Figma, "+
			"so selecting several frames returns their names paired with links.",
		bridge.DefaultTimeout)
	registerBridged[nodeInfoArgs](s, b, "get_design_context",
		"Get detailed information about a node (geometry, fills, text content) including its children up to the given depth.",
		bridge.DefaultTimeout)
	registerBridged[createShapeArgs](s, b, "create_frame",
		"Create a new frame at the given position and size. Returns the created node's summary including its id.",
		bridge.DefaultTimeout)
	registerBridged[createShapeArgs](s, b, "create_rectangle",
		"Create a new rectangle at the given position and size. Returns the created node's summary including its id.",
		bridge.DefaultTimeout)
	registerBridged[createShapeArgs](s, b, "create_ellipse",
		"Create a new ellipse (or circle, when width equals height) at the given position and size.",
		bridge.DefaultTimeout)
	registerBridged[createLineArgs](s, b, "create_line",
		"Create a straight line (e.g. a divider) with the given length, color and thickness.",
		bridge.DefaultTimeout)
	registerBridged[createTextArgs](s, b, "create_text",
		"Create a new text layer with the given content, optionally with a specific font family and style.",
		bridge.DefaultTimeout)
	registerBridged[findArgs](s, b, "find_nodes",
		"Find nodes in the current page (or a subtree) by layer name, text content and/or node type. "+
			"Use this instead of walking the tree in large files.",
		bridge.DefaultTimeout)
	registerBridged[groupArgs](s, b, "group_nodes",
		"Group the given nodes into a new GROUP in the first node's parent.",
		bridge.DefaultTimeout)
	registerBridged[ungroupArgs](s, b, "ungroup_nodes",
		"Ungroup GROUP or FRAME nodes, releasing their children into the parent.",
		bridge.DefaultTimeout)
	registerBridged[appendChildrenArgs](s, b, "append_children",
		"Move nodes into a different parent (reparent), optionally at a specific child index.",
		bridge.DefaultTimeout)
	registerBridged[cloneArgs](s, b, "clone_node",
		"Clone a node, optionally repositioning the copy or appending it to a different parent.",
		bridge.DefaultTimeout)
	registerBridged[listFontsArgs](s, b, "list_available_fonts",
		"List font families (and their styles) available in Figma, optionally filtered by family name substring.",
		bridge.DefaultTimeout)
	registerBridged[createComponentArgs](s, b, "create_component",
		"Convert an existing FRAME into a reusable COMPONENT.",
		bridge.DefaultTimeout)
	// Searching every page can be slow in big files, so give it the long timeout.
	registerBridged[emptyArgs](s, b, "get_local_components",
		"List all local COMPONENT and COMPONENT_SET nodes in the file, across all pages.",
		screenshotTimeout)
	registerBridged[createInstanceArgs](s, b, "create_instance",
		"Create an instance of a COMPONENT at the given position.",
		bridge.DefaultTimeout)
	registerBridged[swapComponentArgs](s, b, "swap_component",
		"Swap an INSTANCE to a different COMPONENT, keeping applicable overrides.",
		bridge.DefaultTimeout)
	registerBridged[detachInstanceArgs](s, b, "detach_instance",
		"Detach an INSTANCE from its component, turning it into a plain frame.",
		bridge.DefaultTimeout)
	registerBridged[emptyArgs](s, b, "get_local_styles",
		"List the file's local paint, text, effect and grid styles with their IDs.",
		bridge.DefaultTimeout)
	registerBridged[paintStyleArgs](s, b, "create_paint_style",
		"Create a named paint (color) style with a solid color.",
		bridge.DefaultTimeout)
	registerBridged[textStyleArgs](s, b, "create_text_style",
		"Create a named text style with a font family, style and size.",
		bridge.DefaultTimeout)
	registerBridged[effectStyleArgs](s, b, "create_effect_style",
		"Create a named effect style (shadows and blurs).",
		bridge.DefaultTimeout)
	registerBridged[applyStyleArgs](s, b, "apply_style",
		"Apply a local style to a node; PAINT styles go to the fill by default or the stroke via target.",
		bridge.DefaultTimeout)
	registerBridged[emptyArgs](s, b, "get_variable_defs",
		"Get all local variable collections (with modes) and variables (design tokens) with their values per mode.",
		bridge.DefaultTimeout)
	registerBridged[variableCollectionArgs](s, b, "create_variable_collection",
		"Create a variable collection for design tokens, optionally naming its default mode (e.g. light).",
		bridge.DefaultTimeout)
	registerBridged[addVariableModeArgs](s, b, "add_variable_mode",
		"Add a mode (e.g. dark) to a variable collection.",
		bridge.DefaultTimeout)
	registerBridged[createVariableArgs](s, b, "create_variable",
		"Create a COLOR, FLOAT, STRING or BOOLEAN variable in a collection, optionally with an initial value.",
		bridge.DefaultTimeout)
	registerBridged[setVariableValueArgs](s, b, "set_variable_value",
		"Set a variable's value for a mode (hex string for COLOR variables).",
		bridge.DefaultTimeout)
	registerBridged[setBoundVariableArgs](s, b, "set_bound_variable",
		"Bind a variable to a node field such as fills, strokes, width, opacity or cornerRadius.",
		bridge.DefaultTimeout)
	registerBridged[setTextArgs](s, b, "set_characters",
		"Replace the text content (characters) of an existing TEXT node.",
		bridge.DefaultTimeout)
	registerBridged[setFillArgs](s, b, "set_fills",
		"Set a solid color (hex) or a linear/radial gradient fill on a node.",
		bridge.DefaultTimeout)
	registerBridged[moveArgs](s, b, "move_nodes",
		"Move one or more nodes to new x/y positions within their parents.",
		bridge.DefaultTimeout)
	registerBridged[resizeArgs](s, b, "resize_nodes",
		"Resize one or more nodes to the given widths and heights.",
		bridge.DefaultTimeout)
	registerBridged[deleteArgs](s, b, "remove_nodes",
		"Remove (delete) one or more nodes from the document.",
		bridge.DefaultTimeout)
	registerBridged[autoLayoutArgs](s, b, "set_auto_layout",
		"Set or remove auto-layout (flexbox-style) on a frame: direction, spacing, padding, alignment and sizing modes.",
		bridge.DefaultTimeout)
	registerBridged[cornerRadiusArgs](s, b, "set_corner_radius",
		"Set corner radius on a node, either uniform or per-corner.",
		bridge.DefaultTimeout)
	registerBridged[strokesArgs](s, b, "set_strokes",
		"Set a solid stroke (border) color, weight and alignment on a node, or remove strokes by omitting the color.",
		bridge.DefaultTimeout)
	registerBridged[effectsArgs](s, b, "set_effects",
		"Set drop shadow, inner shadow or blur effects on a node, replacing its existing effects.",
		bridge.DefaultTimeout)
	registerBridged[setSelectionArgs](s, b, "set_selection",
		"Select the given nodes in Figma and scroll the viewport to show them.",
		bridge.DefaultTimeout)

	// get_screenshot returns an image, so it can't use registerBridged.
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_screenshot",
		Description: "Export a PNG screenshot of a node (or the current selection, or the whole page) " +
			"and return it as an image. Use this to visually verify designs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args screenshotArgs) (*mcp.CallToolResult, any, error) {
		raw, err := b.Call(ctx, args.File, "get_screenshot", args, screenshotTimeout)
		if err != nil {
			return nil, nil, err
		}
		var res screenshotResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, nil, fmt.Errorf("bad screenshot payload from plugin: %w", err)
		}
		png, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("decode screenshot base64: %w", err)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.ImageContent{Data: png, MIMEType: "image/png"},
				&mcp.TextContent{Text: fmt.Sprintf("%.0fx%.0f px", res.Width, res.Height)},
			},
		}, nil, nil
	})

	// import_image reads the image bytes here (the sandboxed plugin has no
	// filesystem or free network access) and ships them to Figma as base64.
	mcp.AddTool(s, &mcp.Tool{
		Name: "import_image",
		Description: "Import an image from a URL or a project file into Figma, either as the fill of an " +
			"existing node or as a new rectangle sized to the image.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args importImageArgs) (*mcp.CallToolResult, any, error) {
		data, err := loadImageBytes(ctx, args.Source)
		if err != nil {
			return nil, nil, err
		}
		raw, err := b.Call(ctx, args.File, "import_image", map[string]any{
			"data":       base64.StdEncoding.EncodeToString(data),
			"node_id":    args.NodeID,
			"x":          args.X,
			"y":          args.Y,
			"width":      args.Width,
			"height":     args.Height,
			"scale_mode": args.ScaleMode,
			"name":       args.Name,
			"parent_id":  args.ParentID,
		}, screenshotTimeout)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		}, nil, nil
	})

	// download_assets writes the exported files to disk, so the file I/O
	// happens here rather than in the sandboxed plugin.
	mcp.AddTool(s, &mcp.Tool{
		Name: "download_assets",
		Description: "Export one or more nodes as SVG, PNG or JPG images and save each to a file inside the " +
			"project directory. Use this to bring icons and images from Figma into the codebase. " +
			"When neither the user nor the project's conventions dictate where assets go, omit path and files " +
			"land in assets/icons/<layer-name>.svg for SVG or assets/images/<layer-name>.<ext> for PNG/JPG; " +
			"an explicit path always wins. " +
			"Pick the format per node based on its content (check type and fills via get_design_context first): " +
			"SVG for pure vector content — icons, logos, shape/path illustrations — it stays sharp and editable; " +
			"PNG at scale 2 for anything containing raster IMAGE fills, photos, screenshots, or blur/shadow effects, " +
			"which SVG cannot represent; JPG only for large photos where file size matters.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args downloadArgs) (*mcp.CallToolResult, any, error) {
		if len(args.Items) == 0 {
			return nil, nil, fmt.Errorf("items is required")
		}
		// Validate every explicit destination before exporting anything.
		// Items without a path get a default one after the export, derived
		// from the layer name the plugin reports.
		dests := make([]string, len(args.Items))
		pluginItems := make([]map[string]any, len(args.Items))
		for i, item := range args.Items {
			if item.Path != "" {
				dest, err := resolveProjectPath(item.Path)
				if err != nil {
					return nil, nil, err
				}
				dests[i] = dest
			}
			format := strings.ToUpper(item.Format)
			if format == "" {
				switch strings.ToLower(filepath.Ext(item.Path)) {
				case ".svg":
					format = "SVG"
				case ".jpg", ".jpeg":
					format = "JPG"
				default:
					format = "PNG"
				}
			}
			pluginItems[i] = map[string]any{
				"node_id": item.NodeID,
				"format":  format,
				"scale":   item.Scale,
			}
		}
		raw, err := b.Call(ctx, args.File, "download_assets", map[string]any{"items": pluginItems}, downloadTimeout(len(args.Items)))
		if err != nil {
			return nil, nil, err
		}
		var results []exportResult
		if err := json.Unmarshal(raw, &results); err != nil {
			return nil, nil, fmt.Errorf("bad export payload from plugin: %w", err)
		}
		if len(results) != len(args.Items) {
			return nil, nil, fmt.Errorf("plugin returned %d assets for %d items", len(results), len(args.Items))
		}
		var lines []string
		failed := 0
		defaultNames := map[string]int{}
		for i, res := range results {
			if res.Error != "" {
				failed++
				lines = append(lines, fmt.Sprintf("FAILED %s (node %s): %s", args.Items[i].Path, args.Items[i].NodeID, res.Error))
				continue
			}
			if dests[i] == "" {
				base := assetFileName(res.Name)
				if base == "" {
					base = "asset"
				}
				ext := "." + strings.ToLower(res.Format)
				if n := defaultNames[base+ext]; n > 0 {
					defaultNames[base+ext] = n + 1
					base = fmt.Sprintf("%s-%d", base, n+1)
				} else {
					defaultNames[base+ext] = 1
				}
				dir := "assets/images/"
				if res.Format == "SVG" {
					dir = "assets/icons/"
				}
				dest, err := resolveProjectPath(dir + base + ext)
				if err != nil {
					return nil, nil, err
				}
				dests[i] = dest
			}
			data, err := base64.StdEncoding.DecodeString(res.Data)
			if err != nil {
				return nil, nil, fmt.Errorf("decode asset %d base64: %w", i, err)
			}
			if err := os.MkdirAll(filepath.Dir(dests[i]), 0o755); err != nil {
				return nil, nil, fmt.Errorf("create directory: %w", err)
			}
			if err := os.WriteFile(dests[i], data, 0o644); err != nil {
				return nil, nil, fmt.Errorf("write file: %w", err)
			}
			lines = append(lines, fmt.Sprintf("Exported %q (%.0fx%.0f px) as %s to %s (%d bytes)",
				res.Name, res.Width, res.Height, res.Format, dests[i], len(data)))
		}
		if failed == len(args.Items) {
			return nil, nil, fmt.Errorf("all %d exports failed:\n%s", failed, strings.Join(lines, "\n"))
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(lines, "\n")}},
		}, nil, nil
	})
}

// maxImageBytes caps import_image payloads; Figma itself rejects images
// larger than 4096x4096.
const maxImageBytes = 20 << 20

// loadImageBytes fetches an http(s) URL or reads a file inside the project.
func loadImageBytes(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("bad image URL: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch image: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch image: %s returned %s", source, resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read image: %w", err)
		}
		if len(data) > maxImageBytes {
			return nil, fmt.Errorf("image at %s exceeds %d MB", source, maxImageBytes>>20)
		}
		return data, nil
	}
	path, err := resolveProjectPath(source)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image file: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image file %s exceeds %d MB", source, maxImageBytes>>20)
	}
	return data, nil
}

// resolveProjectPath resolves p against the current working directory (the
// project the MCP client launched us in) and rejects paths that escape it.
// assetFileName turns a Figma layer name like "icon/Arrow Right" into
// "arrow-right" for the default export path. The part before the last
// slash is a naming-convention prefix, not part of the asset's name.
func assetFileName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func resolveProjectPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(root, p)
	if filepath.IsAbs(p) {
		dest = filepath.Clean(p)
	}
	rel, err := filepath.Rel(root, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the project directory %s", p, root)
	}
	return dest, nil
}
