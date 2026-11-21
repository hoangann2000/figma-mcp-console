package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// designNode is the slice of a get_design_context node that diff_design
// compares: what a tester would see change, not Figma's internal fields.
type designNode struct {
	id, name, typ, parent string
	props                 map[string]string // property label -> display value
	// computed holds the geometry auto layout decides (x, y, width, height):
	// it follows from other edits, so a change there is not reported.
	computed map[string]bool
}

// maxDiffLines keeps a redesign from flooding the caller; the summary line
// still counts everything.
const maxDiffLines = 300

// flattenDesign walks a full (non-compact) get_design_context tree into
// nodes in document order.
func flattenDesign(raw []byte) ([]designNode, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("not a get_design_context JSON tree: %w", err)
	}
	var out []designNode
	var walk func(n map[string]any, parent string, inAuto bool)
	walk = func(n map[string]any, parent string, inAuto bool) {
		id, _ := n["id"].(string)
		name, _ := n["name"].(string)
		typ, _ := n["type"].(string)
		computed := map[string]bool{}
		if inAuto && n["layoutPositioning"] != "ABSOLUTE" {
			computed["x"], computed["y"] = true, true
		}
		for k, sizing := range map[string]string{"width": "layoutSizingHorizontal", "height": "layoutSizingVertical"} {
			if v := n[sizing]; v == "FILL" || v == "HUG" {
				computed[k] = true
			}
		}
		out = append(out, designNode{id: id, name: name, typ: typ, parent: parent, props: nodeProps(n), computed: computed})
		auto := n["layoutMode"] == "HORIZONTAL" || n["layoutMode"] == "VERTICAL"
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				walk(km, id, auto)
			}
		}
	}
	walk(root, "", false)
	return out, nil
}

func nodeProps(n map[string]any) map[string]string {
	p := map[string]string{}
	// Sizes ignore sub-pixel noise from Figma's float math.
	num := func(k string) {
		if v, ok := n[k].(float64); ok {
			p[k] = fmt.Sprintf("%g", math.Round(v*10)/10)
		}
	}
	for _, k := range []string{"x", "y", "width", "height", "opacity", "cornerRadius", "strokeWeight", "fontSize", "itemSpacing"} {
		num(k)
	}
	if v, ok := n["visible"].(bool); ok && !v {
		p["visible"] = "hidden"
	} else {
		p["visible"] = "shown"
	}
	if t, ok := n["text"].(string); ok {
		p["text"] = t
	}
	if f, ok := n["fills"].([]any); ok {
		p["fill"] = paints(f)
	}
	if s, ok := n["strokes"].([]any); ok {
		p["stroke"] = paints(s)
	}
	if d, ok := n["dashPattern"].([]any); ok && len(d) > 0 {
		p["dash"] = fmt.Sprint(d...) // Sprint spaces numbers: "4 2"
	}
	if fn, ok := n["fontName"].(map[string]any); ok {
		p["font"] = fmt.Sprintf("%v %v", fn["family"], fn["style"])
	}
	if pad, ok := n["padding"].(map[string]any); ok {
		p["padding"] = fmt.Sprintf("%v %v %v %v", pad["top"], pad["right"], pad["bottom"], pad["left"])
	}
	if v, ok := n["variant"].(map[string]any); ok {
		p["state"] = sortedPairs(v)
	}
	if as, ok := n["annotations"].([]any); ok {
		var labels []string
		for _, a := range as {
			if am, ok := a.(map[string]any); ok {
				labels = append(labels, fmt.Sprint(am["label"]))
			}
		}
		p["note"] = strings.Join(labels, " | ")
	}
	return p
}

// paints renders visible paints as "#rrggbb" (with opacity when not 100%),
// or the paint type for gradients and images.
func paints(list []any) string {
	var parts []string
	for _, x := range list {
		pm, ok := x.(map[string]any)
		if !ok || pm["visible"] == false {
			continue
		}
		typ, _ := pm["type"].(string)
		c, isColor := pm["color"].(map[string]any)
		if typ != "SOLID" || !isColor {
			parts = append(parts, typ)
			continue
		}
		ch := func(k string) int { v, _ := c[k].(float64); return int(math.Round(v * 255)) }
		s := fmt.Sprintf("#%02x%02x%02x", ch("r"), ch("g"), ch("b"))
		if o, ok := pm["opacity"].(float64); ok && o < 0.995 {
			s += fmt.Sprintf(" %d%%", int(math.Round(o*100)))
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func sortedPairs(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, m[k])
	}
	return strings.Join(parts, ", ")
}

// designChange is one layer added ("+"), removed ("-") or changed ("~").
type designChange struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"` // what changed, or the new text of an added layer
	line   string
}

// diffChanges lists the layers added, removed and changed between two
// get_design_context trees of the same frame, matched by node id.
func diffChanges(before, after []byte) (nOld, nCur int, changes []designChange, err error) {
	old, err := flattenDesign(before)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("baseline: %w", err)
	}
	cur, err := flattenDesign(after)
	if err != nil {
		return 0, 0, nil, err
	}
	oldBy := map[string]designNode{}
	for _, n := range old {
		oldBy[n.id] = n
	}
	curBy := map[string]designNode{}
	names := map[string]string{} // id -> name, for "in <parent>"
	for _, n := range old {
		names[n.id] = n.name
	}
	for _, n := range cur {
		curBy[n.id] = n
		names[n.id] = n.name
	}
	label := func(n designNode) string {
		s := fmt.Sprintf("%s %q (%s)", n.typ, n.name, n.id)
		if pn := names[n.parent]; pn != "" {
			s += fmt.Sprintf(" in %q", pn)
		}
		return s
	}

	var added, removed, changed []designChange
	for _, n := range cur {
		o, ok := oldBy[n.id]
		if !ok {
			c := designChange{Kind: "+", ID: n.id, Name: n.name, line: "+ " + label(n)}
			if t := n.props["text"]; t != "" {
				c.Detail = "text " + clip(t)
				c.line += ": " + c.Detail
			}
			added = append(added, c)
			continue
		}
		var diffs []string
		if o.name != n.name {
			diffs = append(diffs, fmt.Sprintf("name %q → %q", o.name, n.name))
		}
		keys := make([]string, 0, len(n.props))
		for k := range n.props {
			keys = append(keys, k)
		}
		for k := range o.props {
			if _, ok := n.props[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		// A hidden layer's position and size are stale: when it is or was hidden,
		// only showing or hiding it is news.
		hidden := o.props["visible"] == "hidden" || n.props["visible"] == "hidden"
		for _, k := range keys {
			a, b := o.props[k], n.props[k]
			if a == b {
				continue
			}
			geo := k == "x" || k == "y" || k == "width" || k == "height"
			if geo && (hidden || n.computed[k]) {
				continue
			}
			if k == "visible" {
				diffs = append(diffs, b) // "shown" or "hidden"
			} else if k == "text" {
				diffs = append(diffs, fmt.Sprintf("text %s → %s", clip(a), clip(b)))
			} else {
				diffs = append(diffs, fmt.Sprintf("%s %s → %s", k, orNone(a), orNone(b)))
			}
		}
		if len(diffs) > 0 {
			d := strings.Join(diffs, "; ")
			changed = append(changed, designChange{Kind: "~", ID: n.id, Name: n.name, Detail: d, line: "~ " + label(n) + ": " + d})
		}
	}
	for _, o := range old {
		if _, ok := curBy[o.id]; !ok {
			removed = append(removed, designChange{Kind: "-", ID: o.id, Name: o.name, line: "- " + label(o)})
		}
	}
	return len(old), len(cur), append(append(added, removed...), changed...), nil
}

// diffDesign is diffChanges as a text report for an AI client.
func diffDesign(before, after []byte) (string, error) {
	nOld, nCur, changes, err := diffChanges(before, after)
	if err != nil {
		return "", err
	}
	by := map[string][]string{}
	for _, c := range changes {
		by[c.Kind] = append(by[c.Kind], c.line)
	}
	added, removed, changed := by["+"], by["-"], by["~"]

	var b strings.Builder
	fmt.Fprintf(&b, "%d nodes before, %d now: %d added, %d removed, %d changed.\n",
		nOld, nCur, len(added), len(removed), len(changed))
	if len(changes) == 0 {
		b.WriteString("No design changes.\n")
		return b.String(), nil
	}
	written := 0
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for _, l := range lines {
			if written == maxDiffLines {
				fmt.Fprintf(&b, "… output stopped at %d lines; diff a smaller frame for the rest.\n", maxDiffLines)
				written++
				return
			}
			if written > maxDiffLines {
				return
			}
			b.WriteString(l + "\n")
			written++
		}
	}
	section("Added", added)
	section("Removed", removed)
	section("Changed", changed)
	return b.String(), nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// clip keeps long texts on one readable line.
func clip(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if r := []rune(s); len(r) > 80 {
		return fmt.Sprintf("%q…", string(r[:80]))
	}
	return fmt.Sprintf("%q", s)
}
