// Package tools registers the MCP tools that proxy commands to the Figma
// plugin over the bridge.
package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // image.DecodeConfig for download_assets
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

// screenshotTimeout also has to cover the wait behind other sessions driving
// the same plugin: exporting a tall frame is slow, and Figma runs one command
// at a time.
const screenshotTimeout = 150 * time.Second

// maxScreenshotDimension is the largest max_dimension the plugin honours.
const maxScreenshotDimension = 4096

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

// nodeTarget is the shared shape of every tool that mutates existing nodes:
// one node or many in a single call, and a compact result by default so a
// 50-node batch does not bury the caller in echoed summaries.
type nodeTarget struct {
	NodeID  string   `json:"node_id,omitempty" jsonschema:"target node ID; use node_ids to apply the same change to many nodes at once"`
	NodeIDs []string `json:"node_ids,omitempty" jsonschema:"target node IDs, applied in one call (preferred over repeating the tool per node)"`
	Verbose bool     `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the changed ids"`
}

type nodeInfoArgs struct {
	fileArg
	NodeID  string `json:"node_id" jsonschema:"Figma node ID, e.g. \"12:34\""`
	Depth   int    `json:"depth,omitempty" jsonschema:"how many levels of children to include (default 2)"`
	Compact bool   `json:"compact,omitempty" jsonschema:"colors as hex and defaults omitted; use it to read a design, omit it when a script needs raw paint objects"`
	// VisibleOnly is applied by the plugin; Out is handled here.
	VisibleOnly bool   `json:"visible_only,omitempty" jsonschema:"leave out hidden layers and their children"`
	Out         string `json:"out,omitempty" jsonschema:"save the JSON to this file path inside the project instead of returning it, e.g. testing/login/figma-src/original-1440.json"`
}

type diffDesignArgs struct {
	fileArg
	NodeID   string `json:"node_id" jsonschema:"frame to compare, e.g. \"12:34\""`
	Baseline string `json:"baseline,omitempty" jsonschema:"omit to use the checkpoint the user saved from the plugin window. Or a snapshot path inside the project, e.g. testing/login/design/12-34.json.gz; created on the first run, gzip-compressed when the path ends in .gz (about 20x smaller)"`
	Update   bool   `json:"update,omitempty" jsonschema:"after comparing, replace the baseline with the current design so the next run compares against today"`
}

// scanArgs targets one frame or section to read through.
type scanArgs struct {
	fileArg
	NodeID string `json:"node_id" jsonschema:"frame or section to scan, e.g. \"12:34\""`
}

type createShapeArgs struct {
	fileArg
	Name string `json:"name,omitempty" jsonschema:"layer name"`
	// Pointers so "not given" reaches the plugin: create_frame places a screen
	// in free space only when the caller named no position.
	X         *float64 `json:"x,omitempty" jsonschema:"x position in the parent's coordinates (default 0). For a new top-level frame, omit x and y to place it in free space beside the existing frames instead of on top of them"`
	Y         *float64 `json:"y,omitempty" jsonschema:"y position in the parent's coordinates (default 0)"`
	Width     float64  `json:"width" jsonschema:"width in pixels"`
	Height    float64  `json:"height" jsonschema:"height in pixels"`
	FillColor string   `json:"fill_color,omitempty" jsonschema:"solid fill as hex, e.g. #1E90FF, or \"none\" for no fill. Default for frames: white at the top level (a screen), no fill when nested in another frame (a layout wrapper)"`
	ParentID  string   `json:"parent_id,omitempty" jsonschema:"node ID to append into (default: current page)"`
}

type auditArgs struct {
	fileArg
	NodeID     string   `json:"node_id" jsonschema:"frame, section or component to audit, e.g. \"12:34\""`
	Checks     []string `json:"checks,omitempty" jsonschema:"which checks to run: layout, contrast (default: both)"`
	MinTouch   float64  `json:"min_touch,omitempty" jsonschema:"also report controls smaller than this many pixels on either side, e.g. 44 for a mobile screen. Off by default: desktop designs fail it by design"`
	MaxResults int      `json:"max_results,omitempty" jsonschema:"maximum findings to list (default 100); the summary always counts all of them"`
}

type batchOp struct {
	Command string         `json:"command" jsonschema:"tool name to run, e.g. create_frame, create_text, set_auto_layout"`
	Params  map[string]any `json:"params,omitempty" jsonschema:"that tool's arguments, minus file. Any string value \"$N\" is replaced by the id of the node op N produced (0-based), e.g. parent_id: \"$0\""`
}

type batchArgs struct {
	fileArg
	Ops         []batchOp `json:"ops" jsonschema:"commands to run in order"`
	StopOnError bool      `json:"stop_on_error,omitempty" jsonschema:"stop at the first failed op instead of running the rest. What already ran is kept either way"`
}

type exportTokensArgs struct {
	fileArg
	Format string `json:"format,omitempty" jsonschema:"css (default): custom properties, the default mode under :root and every other mode under [data-theme=\"<mode>\"]. json: a W3C design-tokens tree ($type/$value) for one mode"`
	Mode   string `json:"mode,omitempty" jsonschema:"export only this mode, by name, e.g. dark (default: every mode for css, each collection's default mode for json)"`
	Out    string `json:"out,omitempty" jsonschema:"save to this file path inside the project instead of returning it, e.g. src/styles/tokens.css"`
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

type setPageArgs struct {
	fileArg
	Page string `json:"page" jsonschema:"page to open: its name (case-insensitive, unique substring ok) or id, see list_pages"`
}

type findArgs struct {
	fileArg
	Page       string   `json:"page,omitempty" jsonschema:"search this whole page instead of the open one: name or id, see list_pages. Ignored when node_id is given"`
	NodeID     string   `json:"node_id,omitempty" jsonschema:"subtree to search in (default: the page the user currently has open)"`
	Name       string   `json:"name,omitempty" jsonschema:"case-insensitive substring to match against layer names"`
	Text       string   `json:"text,omitempty" jsonschema:"case-insensitive substring to match against TEXT node content"`
	Types      []string `json:"types,omitempty" jsonschema:"node types to include, e.g. FRAME, TEXT, COMPONENT, INSTANCE, RECTANGLE"`
	Fill       string   `json:"fill,omitempty" jsonschema:"keep only nodes whose first visible solid fill is this hex, e.g. #FFFFFF"`
	FontSize   *float64 `json:"font_size,omitempty" jsonschema:"keep only TEXT nodes with this font size"`
	FontStyle  string   `json:"font_style,omitempty" jsonschema:"keep only TEXT nodes with this font style, e.g. Bold"`
	Fields     []string `json:"fields,omitempty" jsonschema:"extra properties to return per node: fills, strokes, fontSize, fontName, cornerRadius, effects, opacity, characters"`
	MaxResults int      `json:"max_results,omitempty" jsonschema:"maximum nodes to return (default 50)"`
}

type renameItem struct {
	NodeID string `json:"node_id" jsonschema:"node to rename"`
	Name   string `json:"name" jsonschema:"new layer name"`
}

type renameArgs struct {
	fileArg
	Items   []renameItem `json:"items" jsonschema:"nodes to rename with their new names"`
	Verbose bool         `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the changed ids"`
}

type textPropertiesArgs struct {
	fileArg
	nodeTarget
	FontFamily    string   `json:"font_family,omitempty" jsonschema:"font family, e.g. Roboto; check with list_available_fonts"`
	FontStyle     string   `json:"font_style,omitempty" jsonschema:"font style, e.g. Bold, Medium, Italic"`
	FontSize      float64  `json:"font_size,omitempty" jsonschema:"font size in pixels"`
	LineHeight    *float64 `json:"line_height,omitempty" jsonschema:"line height in pixels"`
	LetterSpacing *float64 `json:"letter_spacing,omitempty" jsonschema:"letter spacing in pixels (may be negative)"`
	TextAlign     string   `json:"text_align,omitempty" jsonschema:"LEFT, CENTER, RIGHT, or JUSTIFIED"`
	FillColor     string   `json:"fill_color,omitempty" jsonschema:"text color as hex, e.g. #111111"`
}

type combineVariantsArgs struct {
	fileArg
	NodeIDs  []string `json:"node_ids" jsonschema:"COMPONENT nodes to combine into one variant set"`
	Name     string   `json:"name,omitempty" jsonschema:"name for the component set"`
	ParentID string   `json:"parent_id,omitempty" jsonschema:"parent for the set (default: the first component's parent)"`
}

type componentPropertyArgs struct {
	fileArg
	ComponentID  string `json:"component_id" jsonschema:"COMPONENT or COMPONENT_SET to add the property to"`
	Name         string `json:"name" jsonschema:"property name as it appears in the instance panel, e.g. Label"`
	Type         string `json:"type,omitempty" jsonschema:"TEXT (default), BOOLEAN, or INSTANCE_SWAP"`
	DefaultValue any    `json:"default_value,omitempty" jsonschema:"default: a string for TEXT, true/false for BOOLEAN, and for INSTANCE_SWAP the COMPONENT's node id such as \"1:908\" (its published key is rejected)"`
	BindNodeID   string `json:"bind_node_id,omitempty" jsonschema:"child layer to drive with this property (its text for TEXT, visibility for BOOLEAN, swap target for INSTANCE_SWAP). Without it the property exists but controls nothing"`
}

type instancePropertiesArgs struct {
	fileArg
	InstanceID string         `json:"instance_id" jsonschema:"INSTANCE node to update"`
	Properties map[string]any `json:"properties" jsonschema:"property name to value. Variant properties use the plain name (Size), TEXT/BOOLEAN/INSTANCE_SWAP use the full key returned by add_component_property (Label#12:3)"`
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
	Verbose  bool     `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the moved ids"`
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
	nodeTarget
	Text string `json:"text" jsonschema:"new text content"`
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
	nodeTarget
	Color    string        `json:"color,omitempty" jsonschema:"solid fill as hex, e.g. #FF5733, or \"none\" to remove all fills (omit when using gradient)"`
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
	Items   []moveItem `json:"items" jsonschema:"nodes to move with their new positions"`
	Verbose bool       `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the changed ids"`
}

type resizeItem struct {
	NodeID string  `json:"node_id" jsonschema:"target node ID"`
	Width  float64 `json:"width" jsonschema:"new width in pixels"`
	Height float64 `json:"height" jsonschema:"new height in pixels"`
}

type resizeArgs struct {
	fileArg
	Items   []resizeItem `json:"items" jsonschema:"nodes to resize with their new sizes"`
	Verbose bool         `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the changed ids"`
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
	Verbose           bool     `json:"verbose,omitempty" jsonschema:"return the full node summary instead of just the id"`
}

type cornerRadiusArgs struct {
	fileArg
	nodeTarget
	Radius      *float64 `json:"radius,omitempty" jsonschema:"uniform radius for all corners"`
	TopLeft     *float64 `json:"top_left,omitempty" jsonschema:"top-left corner radius"`
	TopRight    *float64 `json:"top_right,omitempty" jsonschema:"top-right corner radius"`
	BottomRight *float64 `json:"bottom_right,omitempty" jsonschema:"bottom-right corner radius"`
	BottomLeft  *float64 `json:"bottom_left,omitempty" jsonschema:"bottom-left corner radius"`
}

type strokesArgs struct {
	fileArg
	nodeTarget
	Color   string   `json:"color,omitempty" jsonschema:"solid stroke color as hex, e.g. #333333; omit or \"none\" to remove all strokes"`
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
	nodeTarget
	Effects []effectSpec `json:"effects" jsonschema:"effects to set, replacing existing ones; empty list removes all effects"`
}

type downloadItem struct {
	NodeID string  `json:"node_id" jsonschema:"node to export"`
	Path   string  `json:"path,omitempty" jsonschema:"destination file path relative to the project directory. Omit it to use the defaults: assets/icons/<layer-name>.svg for SVG, assets/images/<layer-name>.<ext> for PNG/JPG. Set it only when the user or the project's asset conventions specify a destination, e.g. public/icons/arrow-right.svg"`
	Format string  `json:"format,omitempty" jsonschema:"SVG, PNG, or JPG (default: from the path extension, else PNG). SVG only suits pure vector nodes; use PNG when the node has IMAGE fills or blur/shadow effects"`
	Scale  float64 `json:"scale,omitempty" jsonschema:"export scale 0.5..4 for PNG/JPG (default 1)"`
	// Figma otherwise crops to what is drawn: a text layer comes out cut
	// tight around its glyphs, smaller than its box.
	ExactBounds bool `json:"exact_bounds,omitempty" jsonschema:"export exactly the node's box (width x height x scale), empty space included and anything drawn outside it, such as a shadow, cut off. Use it when the image is compared pixel by pixel against the node; leave it off for assets"`
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
	nodeTarget
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
	nodeTarget
	Field      string `json:"field" jsonschema:"node field to bind: fills, strokes, width, height, opacity, cornerRadius, itemSpacing, ..."`
	VariableID string `json:"variable_id" jsonschema:"variable to bind to the field"`
}

type prototypeLinkItem struct {
	NodeID        string   `json:"node_id" jsonschema:"source node that reacts, e.g. a button frame or instance"`
	DestinationID string   `json:"destination_id,omitempty" jsonschema:"frame to go to; required for NAVIGATE, SWAP and OVERLAY, and must be omitted for BACK and CLOSE"`
	Trigger       string   `json:"trigger,omitempty" jsonschema:"ON_CLICK (default), ON_HOVER, ON_PRESS or ON_DRAG"`
	Navigation    string   `json:"navigation,omitempty" jsonschema:"NAVIGATE (default), SWAP, OVERLAY, BACK or CLOSE; BACK and CLOSE take no destination_id"`
	Transition    string   `json:"transition,omitempty" jsonschema:"animation between the frames: none by default, else DISSOLVE, SMART_ANIMATE, SCROLL_ANIMATE, MOVE_IN, MOVE_OUT, PUSH, SLIDE_IN or SLIDE_OUT"`
	Direction     string   `json:"direction,omitempty" jsonschema:"LEFT (default), RIGHT, TOP or BOTTOM; only for the directional transitions MOVE_IN, MOVE_OUT, PUSH, SLIDE_IN, SLIDE_OUT"`
	Duration      *float64 `json:"duration,omitempty" jsonschema:"transition duration in seconds (default 0.3); ignored without a transition"`
	Easing        string   `json:"easing,omitempty" jsonschema:"EASE_OUT (default), EASE_IN, EASE_IN_AND_OUT or LINEAR; ignored without a transition"`
}

type prototypeLinksArgs struct {
	fileArg
	Items   []prototypeLinkItem `json:"items" jsonschema:"prototype links to set, one per source node. Several items on the same node become its full set of reactions; the node's existing reactions are replaced"`
	Verbose bool                `json:"verbose,omitempty" jsonschema:"return full node summaries instead of just the changed ids"`
}

type prototypeLinksReadArgs struct {
	fileArg
	NodeID string `json:"node_id,omitempty" jsonschema:"subtree to read reactions from (default: the page the user currently has open)"`
}

type flowStartingPointArgs struct {
	fileArg
	NodeID string `json:"node_id" jsonschema:"FRAME or SECTION to mark as a prototype flow's first screen"`
	Name   string `json:"name,omitempty" jsonschema:"flow name shown in the prototype panel (default: the node's layer name)"`
	Remove bool   `json:"remove,omitempty" jsonschema:"remove this node from the page's flow starting points instead of adding it"`
}

type screenshotArgs struct {
	fileArg
	NodeID       string  `json:"node_id,omitempty" jsonschema:"node to capture (default: current selection, else current page)"`
	Scale        float64 `json:"scale,omitempty" jsonschema:"export scale 0.5..4 (default 1)"`
	MaxDimension float64 `json:"max_dimension,omitempty" jsonschema:"longest side in pixels before the scale is reduced to fit (default 2000, ceiling 4096)"`
}

type screenshotResult struct {
	Data         string  `json:"data"`
	Width        float64 `json:"width"`
	Height       float64 `json:"height"`
	Scale        float64 `json:"scale"`
	Clamped      bool    `json:"clamped"`
	SourceHeight float64 `json:"sourceHeight"`
}

// connectedFile resolves a tool's file argument the way the bridge routes it:
// the only connected file, else a key or exact name, else a unique substring.
func connectedFile(ctx context.Context, b *bridge.Router, ref string) (bridge.FileInfo, error) {
	files, err := b.Files(ctx)
	if err != nil {
		return bridge.FileInfo{}, err
	}
	if len(files) == 0 {
		return bridge.FileInfo{}, bridge.ErrNotConnected
	}
	want := strings.ToLower(strings.TrimSpace(ref))
	if want == "" && len(files) == 1 {
		return files[0], nil
	}
	var exact, partial []bridge.FileInfo
	for _, f := range files {
		key, name := strings.ToLower(f.Key), strings.ToLower(f.Name)
		switch {
		case want == "":
		case key == want || name == want:
			exact = append(exact, f)
		case strings.Contains(name, want):
			partial = append(partial, f)
		}
	}
	if len(exact) == 0 {
		exact = partial
	}
	if len(exact) != 1 {
		return bridge.FileInfo{}, fmt.Errorf("pass the file parameter to choose one connected Figma file (see list_files)")
	}
	return exact[0], nil
}

// Register adds all Figma tools to the MCP server.
func Register(s *mcp.Server, b *bridge.Router) {
	b.SetPluginHook(checkpointHook)
	// list_files is answered by the bridge itself, not a plugin.
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_files",
		Description: "List the Figma files currently connected to the bridge (one per open plugin window). " +
			"When several files are connected, pass one of the returned names as the file parameter of the other tools. " +
			"read_only: true means the user has Read-only ticked in that file's plugin window and every edit will be " +
			"refused: before a task that changes the design, call this first and, if it is read-only, ask the user to " +
			"untick it instead of starting the work.",
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

	registerBridged[emptyArgs](s, b, "list_pages",
		"List the document's pages and which one is currently open. "+
			"Every tool except find_nodes acts on the open page, and the user can change it at any time, so check here before "+
			"assuming where your work will land.",
		bridge.DefaultTimeout)
	registerBridged[setPageArgs](s, b, "set_current_page",
		"Open a page by name or id, so subsequent create/find calls act on it. Use this instead of hoping the right page is open.",
		bridge.DefaultTimeout)
	registerBridged[emptyArgs](s, b, "get_metadata",
		"Get the current Figma document: file name, current page, and a summary of the page's top-level layers.",
		bridge.DefaultTimeout)
	registerBridged[emptyArgs](s, b, "get_selection",
		"Get the nodes currently selected in Figma, with id, name, type, position and size. "+
			"Each item also carries a shareable figma.com link to that frame/section when the file is saved to Figma, "+
			"so selecting several frames returns their names paired with links.",
		bridge.DefaultTimeout)
	// get_design_context can save its result to a file, so it can't use
	// registerBridged.
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_design_context",
		Description: "Get detailed information about a node (geometry, fills, text content, designer annotations, " +
			"variant values, the variable and style names behind its values, dashed borders, and the runs of text " +
			"that mixes fonts, sizes or colors) including its children up to the given depth. Coordinates are relative to the parent. " +
			"To read a design, pass compact: true (colors as hex, defaults omitted; the full form runs to several " +
			"thousand tokens per frame). Omit compact when a script will parse the result, since scripts expect raw " +
			"paint objects. Pass out to save the JSON to a project file instead of returning it, and visible_only to " +
			"drop hidden layers.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args nodeInfoArgs) (*mcp.CallToolResult, any, error) {
		var dest string
		if args.Out != "" {
			var err error
			if dest, err = resolveProjectPath(args.Out); err != nil {
				return nil, nil, err
			}
		}
		raw, err := b.Call(ctx, args.File, "get_design_context", args, bridge.DefaultTimeout)
		if err != nil {
			return nil, nil, err
		}
		text := string(raw)
		if dest != "" {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return nil, nil, fmt.Errorf("create directory: %w", err)
			}
			if err := os.WriteFile(dest, raw, 0o644); err != nil {
				return nil, nil, fmt.Errorf("write file: %w", err)
			}
			text = fmt.Sprintf("Saved node %s (%d nodes, %d bytes) to %s", args.NodeID, strings.Count(text, `"parentId":`), len(raw), dest)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})
	// diff_design reads and writes snapshot files, so it lives here.
	mcp.AddTool(s, &mcp.Tool{
		Name: "diff_design",
		Description: "Find what changed in a frame since a saved snapshot: layers added, removed, or changed in text, " +
			"color, size, position, font, padding, visibility or component state. Omit baseline to compare against the " +
			"checkpoint the user saved for that frame with the plugin window's \"Save checkpoint\" button (only the user " +
			"moves that one). With baseline, the snapshot is a project file: the first run saves it and reports nothing, " +
			"later runs compare against it. Use it to see what a design update affects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args diffDesignArgs) (*mcp.CallToolResult, any, error) {
		text := func(s string) *mcp.CallToolResult {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
		}
		checkpoint := args.Baseline == ""
		var dest string
		var err error
		if checkpoint {
			if args.Update {
				return nil, nil, fmt.Errorf("update needs a baseline path: the checkpoint is moved only by the user, with Save checkpoint in the plugin window")
			}
			file, err := connectedFile(ctx, b, args.File)
			if err != nil {
				return nil, nil, err
			}
			if dest, err = checkpointPath(file, args.NodeID); err != nil {
				return nil, nil, err
			}
		} else if dest, err = resolveProjectPath(args.Baseline); err != nil {
			return nil, nil, err
		}
		// Depth 100 reaches every layer of any real screen.
		cur, err := b.Call(ctx, args.File, "get_design_context",
			map[string]any{"node_id": args.NodeID, "depth": 100}, bridge.DefaultTimeout)
		if err != nil {
			return nil, nil, err
		}
		old, err := readSnapshot(dest)
		if os.IsNotExist(err) {
			if checkpoint {
				return text(fmt.Sprintf("No checkpoint saved for node %s. The user saves one by selecting the frame "+
					"and clicking Save checkpoint in the Figma MCP Console window; changes are tracked from then on.", args.NodeID)), nil, nil
			}
			if err := writeSnapshot(dest, cur); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("No snapshot yet: saved the current design of node %s to %s. "+
				"Run diff_design again after the design changes to see what changed.", args.NodeID, dest)), nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read baseline: %w", err)
		}
		info, _ := os.Stat(dest)
		report, err := diffDesign(old, cur)
		if err != nil {
			return nil, nil, err
		}
		what := dest
		if checkpoint {
			what = "the user's checkpoint"
		}
		head := fmt.Sprintf("Node %s compared with %s (saved %s).\n", args.NodeID, what, info.ModTime().Format("2006-01-02 15:04"))
		if args.Update {
			if err := writeSnapshot(dest, cur); err != nil {
				return nil, nil, err
			}
			report += "\nBaseline replaced with the current design.\n"
		}
		return text(head + report), nil, nil
	})
	registerBridged[scanArgs](s, b, "get_component_states",
		"List every component used inside a frame, grouped by component, with all the states (variant values such as "+
			"State=Default/Error/Disabled) its component set defines and the state each instance is shown in. "+
			"It also lists instances edited by hand on top of their component (overrides: which layer, which fields, "+
			"e.g. fills or characters), the usual sign of a state drawn by hand instead of picked from the variants. "+
			"Use it to find states the design defines but a spec or test plan misses.",
		bridge.DefaultTimeout)
	registerBridged[auditArgs](s, b, "audit_design",
		"Check a frame for defects a screenshot shows but is easy to misread, and return only the findings: layers cut off "+
			"or hidden by a clipping parent, layers sticking out of their parent, text spilling out of or truncated in a fixed box, "+
			"text below WCAG AA contrast (4.5:1, 3:1 for large text) and, with min_touch, controls too small to tap. "+
			"Run it after drawing or changing a screen and fix what it lists before taking a screenshot; an empty list means "+
			"none of these were found, not that the design is right. Read-only: it never changes the file.",
		bridge.DefaultTimeout)
	registerBridged[batchArgs](s, b, "batch",
		"Run several commands in one call, in order: building a screen is one batch instead of dozens of calls. "+
			"Each op is another tool's name and arguments, and \"$N\" in an argument stands for the id of the node op N created, "+
			"so a frame and everything inside it fit in one batch. Each op reports its own result or error; a failed op does not "+
			"undo or stop the others unless stop_on_error is set. Not for get_screenshot, download_assets, import_image, "+
			"diff_design or export_tokens, which run only as their own call.",
		screenshotTimeout)
	registerBridged[createShapeArgs](s, b, "create_frame",
		"Create a new frame at the given position and size. Returns the created node's summary including its id. "+
			"A top-level frame is filled white and, when x and y are omitted, placed beside the existing frames; "+
			"a frame nested in another has no fill unless fill_color is given.",
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
		"Find nodes by layer name, text content, node type, fill color, font size or font style, in a subtree (node_id), a named page (page), "+
			"or whichever page is open. The result names the scope it searched — pass page or node_id when it matters, since the open page can change mid-session. "+
			"Use this instead of walking the tree in large files, and prefer the style filters over guessing a node's role from its size. "+
			"Feed the returned ids straight into a tool's node_ids to edit the whole match set in one call.",
		bridge.DefaultTimeout)
	registerBridged[renameArgs](s, b, "rename_nodes",
		"Rename one or more layers in place. Use this rather than clone_node plus remove_nodes, which breaks every existing instance of a component.",
		bridge.DefaultTimeout)
	registerBridged[textPropertiesArgs](s, b, "set_text_properties",
		"Change the font family, style, size, line height, letter spacing, alignment or color of existing TEXT nodes. "+
			"This is the only way to restyle text after create_text, e.g. to toggle a label between Regular and Bold for a variant.",
		bridge.DefaultTimeout)
	registerBridged[groupArgs](s, b, "group_nodes",
		"Group the given nodes into a new GROUP in the first node's parent.",
		bridge.DefaultTimeout)
	registerBridged[ungroupArgs](s, b, "ungroup_nodes",
		"Ungroup GROUP or FRAME nodes, releasing their children into the parent.",
		bridge.DefaultTimeout)
	registerBridged[appendChildrenArgs](s, b, "append_children",
		"Move nodes into a different parent (reparent), optionally at a specific child index. "+
			"Moving a variant out of its COMPONENT_SET detaches it and Figma renames it to <Set>/<Variant>; the result reports that as a warning.",
		bridge.DefaultTimeout)
	registerBridged[cloneArgs](s, b, "clone_node",
		"Clone a node, optionally repositioning the copy or appending it to a different parent. "+
			"Figma drops the copy of a node that lives in a SECTION onto the page at 0,0 instead of beside the original, "+
			"so pass parent_id and x/y when cloning inside a section.",
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
		"Create an instance of a COMPONENT at the given position. "+
			"Layers inside an instance have derived ids of the form I<instance-id>;<source-child-id>, which find_nodes returns and every tool accepts.",
		bridge.DefaultTimeout)
	registerBridged[combineVariantsArgs](s, b, "combine_as_variants",
		"Combine several COMPONENT nodes into one COMPONENT_SET (variants), laid out in a column and sized to fit them. "+
			"Name each component Property=Value (e.g. \"Size=Large\") beforehand so Figma derives the variant properties from the names.",
		bridge.DefaultTimeout)
	registerBridged[componentPropertyArgs](s, b, "add_component_property",
		"Add a TEXT, BOOLEAN or INSTANCE_SWAP property to a component or component set, and optionally bind it to a child layer "+
			"so the property actually drives that layer. Returns the property key needed by set_instance_properties.",
		bridge.DefaultTimeout)
	registerBridged[instancePropertiesArgs](s, b, "set_instance_properties",
		"Set variant and component property values on an INSTANCE (e.g. switch it to the Large variant, or override its label text). "+
			"An unknown key fails with the list of keys the instance accepts.",
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
		"Set a solid color (hex) or a linear/radial gradient fill on one or many nodes, or remove their fills with color: \"none\".",
		bridge.DefaultTimeout)
	registerBridged[moveArgs](s, b, "move_nodes",
		"Move one or more nodes to new x/y positions within their parents.",
		bridge.DefaultTimeout)
	registerBridged[resizeArgs](s, b, "resize_nodes",
		"Resize one or more nodes to the given widths and heights. "+
			"Children only follow if they have constraints or auto-layout; inside a plain frame or component you must reposition them yourself with move_nodes.",
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
	registerBridged[prototypeLinksArgs](s, b, "set_prototype_links",
		"Wire prototype links (reactions) from source nodes to destination frames in one call: trigger, navigation and an optional transition. "+
			"Each item replaces that node's existing reactions, so pass every link a node should have in the same call. "+
			"BACK and CLOSE take no destination_id; the other navigations require one. "+
			"Figma only accepts prototype links between top-level frames on the same page — a frame nested inside another is rejected.",
		bridge.DefaultTimeout)
	registerBridged[prototypeLinksReadArgs](s, b, "get_prototype_links",
		"List the prototype links in a subtree (or the open page): source node, trigger, navigation and destination. "+
			"Use it to check wiring after set_prototype_links.",
		bridge.DefaultTimeout)
	registerBridged[flowStartingPointArgs](s, b, "set_flow_starting_point",
		"Mark a frame as a prototype flow's starting point on its page, or remove it again with remove: true. "+
			"Returns the page's full list of starting points.",
		bridge.DefaultTimeout)

	// export_tokens renders (and can save) the variables here; the plugin only
	// supplies them.
	mcp.AddTool(s, &mcp.Tool{
		Name: "export_tokens",
		Description: "Export the file's variables (design tokens) as CSS custom properties or a W3C design-tokens JSON tree, " +
			"with aliases kept as references (var(--x) / {x.y}). Use it to bring tokens into a codebase's theme file. " +
			"Numbers are exported without units, since a Figma number may be a length, a weight or an opacity: add px where it is a length.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args exportTokensArgs) (*mcp.CallToolResult, any, error) {
		var dest string
		if args.Out != "" {
			var err error
			if dest, err = resolveProjectPath(args.Out); err != nil {
				return nil, nil, err
			}
		}
		raw, err := b.Call(ctx, args.File, "get_variable_defs", emptyArgs{}, bridge.DefaultTimeout)
		if err != nil {
			return nil, nil, err
		}
		text, count, err := renderTokens(raw, args.Format, args.Mode)
		if err != nil {
			return nil, nil, err
		}
		if dest != "" {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return nil, nil, fmt.Errorf("create directory: %w", err)
			}
			if err := os.WriteFile(dest, []byte(text), 0o644); err != nil {
				return nil, nil, fmt.Errorf("write file: %w", err)
			}
			text = fmt.Sprintf("Saved %d tokens to %s", count, dest)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})

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
		note := fmt.Sprintf("%.0fx%.0f px", res.Width, res.Height)
		if res.Clamped {
			// A tall page squeezed to fit is unreadable; say so instead of
			// handing back a thumbnail that looks like the real thing.
			note += fmt.Sprintf(" — scaled to %.2fx to fit; the node is %.0f px tall, so detail is lost. ",
				res.Scale, res.SourceHeight)
			if args.MaxDimension >= maxScreenshotDimension {
				// Already at the ceiling: suggesting a bigger one wastes a call.
				note += "max_dimension is at its ceiling, so screenshot a child node instead."
			} else {
				note += "Screenshot a child node, or raise max_dimension, to read it."
			}
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.ImageContent{Data: png, MIMEType: "image/png"},
				&mcp.TextContent{Text: note},
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
				"exact":   item.ExactBounds,
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
			// Report the image's real size: shadows and overflowing children
			// make it larger than the node, and callers check it against that.
			size := fmt.Sprintf("node %.0fx%.0f", res.Width, res.Height)
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
				size = fmt.Sprintf("image %dx%d px, %s", cfg.Width, cfg.Height, size)
			}
			lines = append(lines, fmt.Sprintf("Exported %q (%s) as %s to %s (%d bytes)",
				res.Name, size, res.Format, dests[i], len(data)))
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

var imageClient = &http.Client{Timeout: 60 * time.Second}

// loadImageBytes fetches an http(s) URL or reads a file inside the project.
func loadImageBytes(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("bad image URL: %w", err)
		}
		resp, err := imageClient.Do(req)
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
		return "", fmt.Errorf("path %q escapes the project directory %s; pass a path inside it, e.g. assets/icons/arrow.svg", p, root)
	}
	return dest, nil
}
