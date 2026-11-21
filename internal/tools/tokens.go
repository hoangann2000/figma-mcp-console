package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// varDefs is what the plugin's get_variable_defs returns.
type varDefs struct {
	Collections []varCollection `json:"collections"`
	Variables   []struct {
		ID           string                     `json:"id"`
		Name         string                     `json:"name"`
		ResolvedType string                     `json:"resolvedType"`
		CollectionID string                     `json:"collectionId"`
		ValuesByMode map[string]json.RawMessage `json:"valuesByMode"`
	} `json:"variables"`
}

// tokenSlug turns "color/bg/Primary 500" into "color-bg-primary-500".
func tokenSlug(name string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func tokenNumber(f float64) string {
	return strconv.FormatFloat(math.Round(f*10000)/10000, 'f', -1, 64)
}

// tokenValue renders one variable value. css picks the syntax: var(--x) and
// quoted strings for CSS, {x.y} and native JSON types for the tokens tree.
// An alias to a variable this file does not define (a library token) keeps
// its id, which is all the plugin can see of it.
func tokenValue(raw json.RawMessage, names map[string]string, css bool) any {
	var obj struct {
		Type       string  `json:"type"`
		ID         string  `json:"id"`
		R, G, B, A float64 // encoding/json matches r/g/b/a case-insensitively
	}
	if json.Unmarshal(raw, &obj) == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		if obj.Type == "VARIABLE_ALIAS" {
			name, ok := names[obj.ID]
			if !ok {
				name = obj.ID
			}
			if css {
				return "var(--" + tokenSlug(name) + ")"
			}
			return "{" + strings.ReplaceAll(name, "/", ".") + "}"
		}
		ch := func(v float64) int { return int(math.Round(v * 255)) }
		if obj.A < 1 {
			return fmt.Sprintf("rgba(%d, %d, %d, %s)", ch(obj.R), ch(obj.G), ch(obj.B), tokenNumber(obj.A))
		}
		return fmt.Sprintf("#%02x%02x%02x", ch(obj.R), ch(obj.G), ch(obj.B))
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case float64:
		if css {
			return tokenNumber(x)
		}
	case string:
		if css {
			return strconv.Quote(x)
		}
	case bool:
		if css {
			return strconv.FormatBool(x)
		}
	}
	return v
}

var tokenTypes = map[string]string{"COLOR": "color", "FLOAT": "number", "STRING": "string", "BOOLEAN": "boolean"}

// renderTokens turns get_variable_defs output into CSS custom properties or a
// design-tokens JSON tree, and reports how many tokens it wrote.
func renderTokens(raw []byte, format, mode string) (string, int, error) {
	var defs varDefs
	if err := json.Unmarshal(raw, &defs); err != nil {
		return "", 0, fmt.Errorf("bad variable payload from plugin: %w", err)
	}
	if len(defs.Variables) == 0 {
		return "", 0, fmt.Errorf("this file defines no local variables; create some with create_variable, or read styles with get_local_styles")
	}
	format = strings.ToLower(format)
	if format == "" {
		format = "css"
	}
	if format != "css" && format != "json" {
		return "", 0, fmt.Errorf("unknown format %q (use css or json)", format)
	}
	names := map[string]string{}
	for _, v := range defs.Variables {
		names[v.ID] = v.Name
	}

	// selector -> lines, in first-seen order (css); nested tree (json).
	var order []string
	blocks := map[string][]string{}
	tree := map[string]any{}
	var known []string
	count := 0
	for _, c := range defs.Collections {
		for _, m := range c.Modes {
			known = append(known, m.Name)
			isDefault := m.ModeID == c.DefaultModeID
			switch {
			case mode != "" && !strings.EqualFold(mode, m.Name):
				continue
			case mode == "" && format == "json" && !isDefault:
				continue
			}
			selector := ":root"
			if mode == "" && !isDefault {
				selector = fmt.Sprintf("[data-theme=%q]", tokenSlug(m.Name))
			}
			for _, v := range defs.Variables {
				val, ok := v.ValuesByMode[m.ModeID]
				if v.CollectionID != c.ID || !ok {
					continue
				}
				count++
				if format == "css" {
					if _, seen := blocks[selector]; !seen {
						order = append(order, selector)
					}
					blocks[selector] = append(blocks[selector],
						fmt.Sprintf("  --%s: %v;", tokenSlug(v.Name), tokenValue(val, names, true)))
					continue
				}
				// Names can repeat across collections, so each gets its own
				// branch once there is more than one.
				at := tree
				path := strings.Split(v.Name, "/")
				if len(defs.Collections) > 1 {
					path = append([]string{c.Name}, path...)
				}
				for _, seg := range path {
					next, _ := at[seg].(map[string]any)
					if next == nil {
						next = map[string]any{}
						at[seg] = next
					}
					at = next
				}
				at["$type"] = tokenTypes[v.ResolvedType]
				at["$value"] = tokenValue(val, names, false)
			}
		}
	}
	if count == 0 {
		return "", 0, fmt.Errorf("no mode named %q; this file has: %s", mode, strings.Join(known, ", "))
	}
	if format == "json" {
		out, err := json.MarshalIndent(tree, "", "  ")
		return string(out) + "\n", count, err
	}
	var b strings.Builder
	for i, sel := range order {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(sel + " {\n" + strings.Join(blocks[sel], "\n") + "\n}\n")
	}
	return b.String(), count, nil
}
