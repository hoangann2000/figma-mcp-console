package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoangann2000/figma-mcp-console/internal/bridge"
)

// import_tokens reads what export_tokens writes (CSS custom properties or a
// design-tokens JSON tree) and creates or updates variables to match. The
// server works out the plan; one plugin batch carries it out, so a new
// variable and an alias to it land in the same call ("$N" refers back).

// tokenValue as read from a source file: a concrete value of Type, or an
// alias to another token by the name it has in that file.
type importValue struct {
	Type  string // COLOR, FLOAT, STRING, BOOLEAN; empty for an alias
	Value any    // map r/g/b/a for COLOR, float64, string or bool
	Alias string
}

type importToken struct {
	Name   string                 // as written: "color-bg" (CSS) or "color/bg" (JSON)
	Values map[string]importValue // mode name -> value; "" is the default mode
}

var (
	cssComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	themeAttr   = regexp.MustCompile(`data-theme\s*=\s*["']?([^"'\]]+)`)
	classOnly   = regexp.MustCompile(`^\.([A-Za-z0-9_-]+)$`)
	cssVarRef   = regexp.MustCompile(`^var\(\s*--([A-Za-z0-9_-]+)\s*(,.*)?\)$`)
	jsonRef     = regexp.MustCompile(`^\{([^{}]+)\}$`)
	hexColor    = regexp.MustCompile(`^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	rgbFunc     = regexp.MustCompile(`^rgba?\(\s*([^)]*)\)$`)
	pxNumber    = regexp.MustCompile(`^(-?\d*\.?\d+)(px)?$`)
	quotedValue = regexp.MustCompile(`^"(.*)"$|^'(.*)'$`)
)

// parseColor reads #rgb, #rgba, #rrggbb, #rrggbbaa, rgb() and rgba().
func parseColor(s string) (map[string]float64, bool) {
	if m := hexColor.FindStringSubmatch(s); m != nil {
		h := m[1]
		if len(h) <= 4 { // #rgb(a): each digit doubled
			var b strings.Builder
			for _, r := range h {
				b.WriteRune(r)
				b.WriteRune(r)
			}
			h = b.String()
		}
		ch := func(i int) float64 { v, _ := strconv.ParseUint(h[i:i+2], 16, 8); return float64(v) / 255 }
		c := map[string]float64{"r": ch(0), "g": ch(2), "b": ch(4), "a": 1}
		if len(h) == 8 {
			c["a"] = ch(6)
		}
		return c, true
	}
	m := rgbFunc.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	parts := strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
	if len(parts) != 3 && len(parts) != 4 {
		return nil, false
	}
	var v [4]float64
	v[3] = 1
	for i, p := range parts {
		pct := strings.HasSuffix(p, "%")
		f, err := strconv.ParseFloat(strings.TrimSuffix(p, "%"), 64)
		if err != nil {
			return nil, false
		}
		switch {
		case i == 3 && pct:
			f /= 100
		case i < 3 && pct:
			f /= 100
		case i < 3:
			f /= 255
		}
		v[i] = f
	}
	return map[string]float64{"r": v[0], "g": v[1], "b": v[2], "a": v[3]}, true
}

// parseTokenString turns one written value into a typed value or an alias.
// Anything that is not a color, a (px) number or a boolean is kept as text,
// so 1rem or a font stack survives as a STRING rather than being dropped.
func parseTokenString(s string, json bool) importValue {
	s = strings.TrimSpace(s)
	if json {
		if m := jsonRef.FindStringSubmatch(s); m != nil {
			return importValue{Alias: strings.ReplaceAll(m[1], ".", "/")}
		}
	} else if m := cssVarRef.FindStringSubmatch(s); m != nil {
		return importValue{Alias: m[1]}
	}
	if c, ok := parseColor(s); ok {
		return importValue{Type: "COLOR", Value: c}
	}
	if m := pxNumber.FindStringSubmatch(s); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		return importValue{Type: "FLOAT", Value: f}
	}
	if s == "true" || s == "false" {
		return importValue{Type: "BOOLEAN", Value: s == "true"}
	}
	if m := quotedValue.FindStringSubmatch(s); m != nil {
		if u, err := strconv.Unquote(`"` + m[1] + m[2] + `"`); err == nil {
			return importValue{Type: "STRING", Value: u}
		}
		return importValue{Type: "STRING", Value: m[1] + m[2]}
	}
	return importValue{Type: "STRING", Value: s}
}

// parseCSSTokens reads custom properties from :root (the default mode),
// [data-theme="x"] and single-class selectors such as .dark (mode x / dark).
// At-rules and other selectors are skipped and reported.
func parseCSSTokens(src string) ([]importToken, []string) {
	src = cssComment.ReplaceAllString(src, "")
	var warnings []string
	byName := map[string]*importToken{}
	var order []string
	for i := 0; i < len(src); {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		selector := strings.TrimSpace(src[i : i+open])
		// The block ends at its matching brace (an @media wraps nested blocks).
		depth, end := 0, -1
		for j := i + open; j < len(src); j++ {
			if src[j] == '{' {
				depth++
			} else if src[j] == '}' {
				if depth--; depth == 0 {
					end = j
					break
				}
			}
		}
		if end < 0 {
			warnings = append(warnings, fmt.Sprintf("unclosed block after %q: ignored the rest of the file", selector))
			break
		}
		body := src[i+open+1 : end]
		i = end + 1

		mode, ok := "", true
		switch {
		case strings.HasPrefix(selector, "@"):
			ok = false
		case selector == ":root" || selector == "html":
		case themeAttr.MatchString(selector):
			mode = themeAttr.FindStringSubmatch(selector)[1]
		case classOnly.MatchString(selector):
			mode = classOnly.FindStringSubmatch(selector)[1]
		default:
			ok = false
		}
		if !ok {
			if strings.Contains(body, "--") {
				warnings = append(warnings, fmt.Sprintf("skipped %q: tokens are read from :root, [data-theme=...] or a single class such as .dark", selector))
			}
			continue
		}
		for _, decl := range strings.Split(body, ";") {
			name, value, found := strings.Cut(strings.TrimSpace(decl), ":")
			name = strings.TrimSpace(name)
			if !found || !strings.HasPrefix(name, "--") {
				continue
			}
			name = strings.TrimPrefix(name, "--")
			t := byName[name]
			if t == nil {
				t = &importToken{Name: name, Values: map[string]importValue{}}
				byName[name] = t
				order = append(order, name)
			}
			t.Values[mode] = parseTokenString(value, false)
		}
	}
	out := make([]importToken, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, warnings
}

// parseJSONTokens reads a design-tokens tree: every object with a $value is a
// token named by its path ("color/bg/primary"); its value goes to mode.
func parseJSONTokens(src []byte, mode string) ([]importToken, []string, error) {
	var root map[string]any
	if err := json.Unmarshal(src, &root); err != nil {
		return nil, nil, fmt.Errorf("read tokens JSON: %w", err)
	}
	var out []importToken
	var warnings []string
	var walk func(path []string, node map[string]any)
	walk = func(path []string, node map[string]any) {
		if raw, ok := node["$value"]; ok {
			name := strings.Join(path, "/")
			var v importValue
			switch x := raw.(type) {
			case string:
				v = parseTokenString(x, true)
			case float64:
				v = importValue{Type: "FLOAT", Value: x}
			case bool:
				v = importValue{Type: "BOOLEAN", Value: x}
			case map[string]any: // {"value": 16, "unit": "px"} or {"hex": "#..."}
				if h, ok := x["hex"].(string); ok {
					v = parseTokenString(h, true)
				} else if n, ok := x["value"].(float64); ok && (x["unit"] == nil || x["unit"] == "px") {
					v = importValue{Type: "FLOAT", Value: n}
				}
			}
			if v.Type == "" && v.Alias == "" {
				warnings = append(warnings, fmt.Sprintf("skipped %s: unsupported $value %v", name, raw))
				return
			}
			out = append(out, importToken{Name: name, Values: map[string]importValue{mode: v}})
			return
		}
		keys := make([]string, 0, len(node))
		for k := range node {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if child, ok := node[k].(map[string]any); ok && !strings.HasPrefix(k, "$") {
				walk(append(append([]string{}, path...), k), child)
			}
		}
	}
	walk(nil, root)
	return out, warnings, nil
}

// ---------- planning ----------

type importPlan struct {
	Collection       string // target collection for new variables
	CollectionID     string // empty when it has to be created
	DefaultModeID    string // of the target collection, when it exists
	Created, Updated []string
	Unchanged        int
	Skipped          []string
	ModesToAdd       []planMode
	vars             []*planVar
}

type planMode struct{ CollectionID, Name string } // CollectionID "" = the new target collection

type planVar struct {
	skipped      bool // some of its values were skipped
	token        importToken
	id           string // existing variable, or "" to create
	collectionID string // "" = the new target collection
	typ          string
	sets         []planSet
}

type planSet struct {
	mode    string // mode name, "" = default
	value   importValue
	aliasTo *planVar // alias to a token in this import
	aliasID string   // alias to a variable already in the file
}

// modeID finds a collection's mode by name, or by the slug export_tokens
// wrote into [data-theme="..."].
func modeID(c varCollection, name string) string {
	if name == "" {
		return c.DefaultModeID
	}
	for _, m := range c.Modes {
		if strings.EqualFold(m.Name, name) || tokenSlug(m.Name) == tokenSlug(name) {
			return m.ModeID
		}
	}
	return ""
}

type varCollection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultModeID string `json:"defaultModeId"`
	Modes         []struct {
		ModeID string `json:"modeId"`
		Name   string `json:"name"`
	} `json:"modes"`
}

// sameValue compares a planned value with what the file holds for that mode.
func sameValue(raw json.RawMessage, v importValue, aliasID string) bool {
	if raw == nil {
		return false
	}
	if v.Alias != "" {
		var a struct{ Type, ID string }
		return json.Unmarshal(raw, &a) == nil && a.Type == "VARIABLE_ALIAS" && a.ID == aliasID
	}
	switch v.Type {
	case "COLOR":
		var c map[string]float64
		if json.Unmarshal(raw, &c) != nil {
			return false
		}
		want := v.Value.(map[string]float64)
		for _, k := range []string{"r", "g", "b", "a"} {
			got, ok := c[k]
			if !ok && k == "a" {
				got = 1
			}
			if math.Abs(got-want[k]) > 0.5/255 {
				return false
			}
		}
		return true
	default:
		var x any
		return json.Unmarshal(raw, &x) == nil && x == v.Value
	}
}

// planImport matches tokens to the file's variables by name, or by the slug
// export_tokens gave them, and decides what to create, update or skip.
func planImport(defs varDefs, tokens []importToken, collection string) *importPlan {
	p := &importPlan{Collection: collection}
	if p.Collection == "" {
		p.Collection = "Tokens"
		if len(defs.Collections) == 1 {
			p.Collection = defs.Collections[0].Name
		}
	}
	cols := map[string]varCollection{}
	for _, c := range defs.Collections {
		cols[c.ID] = c
		if c.Name == p.Collection {
			p.CollectionID, p.DefaultModeID = c.ID, c.DefaultModeID
		}
	}
	type existing struct{ id, col, typ, name string }
	bySlug := map[string][]existing{}
	values := map[string]map[string]json.RawMessage{}
	for _, v := range defs.Variables {
		bySlug[tokenSlug(v.Name)] = append(bySlug[tokenSlug(v.Name)], existing{v.ID, v.CollectionID, v.ResolvedType, v.Name})
		values[v.ID] = v.ValuesByMode
	}
	find := func(name string) (existing, bool, bool) {
		cands := bySlug[tokenSlug(name)]
		// An exported JSON tree with several collections nests each under its
		// collection's name: "theme/color/primary" means color/primary in theme.
		if len(cands) == 0 {
			if first, rest, ok := strings.Cut(name, "/"); ok {
				for _, c := range defs.Collections {
					if c.Name == first {
						for _, e := range bySlug[tokenSlug(rest)] {
							if e.col == c.ID {
								return e, true, false
							}
						}
					}
				}
			}
		}
		switch len(cands) {
		case 0:
			return existing{}, false, false
		case 1:
			return cands[0], true, false
		}
		for _, e := range cands {
			if e.col == p.CollectionID {
				return e, true, false
			}
		}
		return existing{}, false, true
	}

	// First pass: who each token is, and its type.
	bySlugNew := map[string]*planVar{}
	for _, t := range tokens {
		pv := &planVar{token: t}
		if e, ok, ambiguous := find(t.Name); ambiguous {
			p.Skipped = append(p.Skipped, t.Name+": several variables in different collections have this name; pass collection")
			continue
		} else if ok {
			pv.id, pv.collectionID, pv.typ = e.id, e.col, e.typ
		} else {
			pv.collectionID = p.CollectionID
		}
		p.vars = append(p.vars, pv)
		bySlugNew[tokenSlug(t.Name)] = pv
	}
	// An alias's type is its target's; follow chains a few steps.
	typeOf := func(pv *planVar) string {
		for range 8 {
			for _, v := range pv.token.Values {
				if v.Type != "" {
					return v.Type
				}
			}
			var target string
			for _, v := range pv.token.Values {
				target = v.Alias
				break
			}
			if t, ok := bySlugNew[tokenSlug(target)]; ok && t != pv {
				if t.typ != "" {
					return t.typ
				}
				pv = t
				continue
			}
			if e, ok, _ := find(target); ok {
				return e.typ
			}
			return ""
		}
		return ""
	}
	for _, pv := range p.vars {
		if pv.id == "" {
			pv.typ = typeOf(pv)
		}
	}

	// Second pass: values, checked against types and what the file holds.
	kept := p.vars[:0]
	needMode := map[planMode]bool{}
	for _, pv := range p.vars {
		if pv.typ == "" {
			p.Skipped = append(p.Skipped, pv.token.Name+": alias to a token that is neither in this file nor in the import")
			continue
		}
		modes := make([]string, 0, len(pv.token.Values))
		for m := range pv.token.Values {
			modes = append(modes, m)
		}
		sort.Strings(modes)
		before := len(p.Skipped)
		for _, m := range modes {
			v := pv.token.Values[m]
			set := planSet{mode: m, value: v}
			if v.Alias != "" {
				if t, ok := bySlugNew[tokenSlug(v.Alias)]; ok {
					if t.typ != pv.typ {
						p.Skipped = append(p.Skipped, fmt.Sprintf("%s: alias to %s, a %s, from a %s", pv.token.Name, v.Alias, t.typ, pv.typ))
						continue
					}
					set.aliasTo, set.aliasID = t, t.id // id empty while t is still to be created
				} else if e, ok, _ := find(v.Alias); ok {
					if e.typ != pv.typ {
						p.Skipped = append(p.Skipped, fmt.Sprintf("%s: alias to %s, a %s, from a %s", pv.token.Name, v.Alias, e.typ, pv.typ))
						continue
					}
					set.aliasID = e.id
				} else {
					p.Skipped = append(p.Skipped, pv.token.Name+": alias to unknown token "+v.Alias)
					continue
				}
			} else if v.Type != pv.typ {
				p.Skipped = append(p.Skipped, fmt.Sprintf("%s: value is a %s but the variable is a %s", pv.token.Name, v.Type, pv.typ))
				continue
			}
			var mid string
			if c, ok := cols[pv.collectionID]; ok {
				mid = modeID(c, m)
			}
			if mid == "" && m != "" {
				needMode[planMode{pv.collectionID, m}] = true
			}
			if pv.id != "" && mid != "" && (v.Alias == "" || set.aliasID != "") &&
				sameValue(values[pv.id][mid], v, set.aliasID) {
				continue
			}
			pv.sets = append(pv.sets, set)
		}
		pv.skipped = len(p.Skipped) > before
		kept = append(kept, pv)
	}
	p.vars = kept
	p.classify()
	for m := range needMode {
		p.ModesToAdd = append(p.ModesToAdd, m)
	}
	sort.Slice(p.ModesToAdd, func(i, j int) bool { return p.ModesToAdd[i].Name < p.ModesToAdd[j].Name })
	return p
}

// classify sorts the variables into created, updated and unchanged from the
// values still to write. A new variable with nothing left to write is dropped,
// and one whose values were partly skipped is not "unchanged".
func (p *importPlan) classify() {
	p.Created, p.Updated, p.Unchanged = nil, nil, 0
	kept := p.vars[:0]
	for _, pv := range p.vars {
		switch {
		case pv.id == "" && len(pv.sets) == 0:
			continue
		case pv.id == "":
			p.Created = append(p.Created, pv.token.Name)
		case len(pv.sets) > 0:
			p.Updated = append(p.Updated, pv.token.Name)
		case !pv.skipped:
			p.Unchanged++
		}
		kept = append(kept, pv)
	}
	p.vars = kept
}

// dropMode removes the values for a mode that could not be added, so the
// summary does not count them as written.
func (p *importPlan) dropMode(collectionID, mode, why string) {
	n := 0
	for _, pv := range p.vars {
		if pv.collectionID != collectionID {
			continue
		}
		kept := pv.sets[:0]
		for _, s := range pv.sets {
			if s.mode == mode {
				n++
				pv.skipped = true
				continue
			}
			kept = append(kept, s)
		}
		pv.sets = kept
	}
	p.Skipped = append(p.Skipped, fmt.Sprintf("mode %q: %d values skipped, the mode could not be added (%s)", mode, n, why))
	p.classify()
}

// batchOps turns the plan into one plugin batch: creations first, then the
// values, so an alias can point at a variable created in the same call.
// modeIDs maps (collection id, mode name) to a mode id once modes exist;
// collectionID is the target's id once it exists.
func (p *importPlan) batchOps(collectionID string, modeIDs func(col, name string) string) ([]map[string]any, []string) {
	var ops []map[string]any
	var skipped []string
	ref := map[*planVar]string{}
	for _, pv := range p.vars {
		if pv.id != "" {
			ref[pv] = pv.id
			continue
		}
		ref[pv] = fmt.Sprintf("$%d", len(ops))
		ops = append(ops, map[string]any{"command": "create_variable", "params": map[string]any{
			"name": strings.TrimPrefix(pv.token.Name, "--"), "collection_id": collectionID, "type": pv.typ,
		}})
	}
	for _, pv := range p.vars {
		col := pv.collectionID
		if col == "" {
			col = collectionID
		}
		for _, s := range pv.sets {
			mid := modeIDs(col, s.mode)
			if mid == "" {
				skipped = append(skipped, fmt.Sprintf("%s: mode %q could not be added", pv.token.Name, s.mode))
				continue
			}
			var value any = s.value.Value
			if s.aliasTo != nil {
				value = map[string]any{"type": "VARIABLE_ALIAS", "id": ref[s.aliasTo]}
			} else if s.value.Alias != "" {
				value = map[string]any{"type": "VARIABLE_ALIAS", "id": s.aliasID}
			}
			ops = append(ops, map[string]any{"command": "set_variable_value", "params": map[string]any{
				"variable_id": ref[pv], "mode_id": mid, "value": value,
			}})
		}
	}
	return ops, skipped
}

func (p *importPlan) summary(dryRun bool) string {
	var b strings.Builder
	verb := "Imported"
	if dryRun {
		verb = "Dry run, nothing written. Would import"
	}
	fmt.Fprintf(&b, "%s into collection %q: %d created, %d updated, %d unchanged, %d skipped.\n",
		verb, p.Collection, len(p.Created), len(p.Updated), p.Unchanged, len(p.Skipped))
	list := func(title string, names []string) {
		if len(names) == 0 {
			return
		}
		if len(names) > 40 {
			names = append(names[:40:40], fmt.Sprintf("… and %d more", len(names)-40))
		}
		fmt.Fprintf(&b, "%s: %s\n", title, strings.Join(names, ", "))
	}
	if p.CollectionID == "" && len(p.Created) > 0 {
		fmt.Fprintf(&b, "Collection %q %s created.\n", p.Collection, map[bool]string{true: "would be", false: "was"}[dryRun])
	}
	var modes []string
	for _, m := range p.ModesToAdd {
		modes = append(modes, m.Name)
	}
	list(map[bool]string{true: "Modes to add", false: "Modes added"}[dryRun], modes)
	list("Created", p.Created)
	list("Updated", p.Updated)
	list("Skipped", p.Skipped)
	return b.String()
}

// ---------- the tool ----------

const maxTokensBytes = 5 << 20

// caller is the slice of bridge.Router that import_tokens needs, so tests can
// stand in for Figma.
type caller interface {
	Call(ctx context.Context, file, command string, params any, timeout time.Duration) (json.RawMessage, error)
}

func importTokens(ctx context.Context, b caller, args importTokensArgs) (string, error) {
	src, format := []byte(args.Content), strings.ToLower(args.Format)
	switch {
	case (args.Path == "") == (args.Content == ""):
		return "", fmt.Errorf("pass either path (a tokens file in the project) or content")
	case args.Path != "":
		p, err := resolveProjectPath(args.Path)
		if err != nil {
			return "", err
		}
		if src, err = os.ReadFile(p); err != nil {
			return "", fmt.Errorf("read tokens file: %w", err)
		}
		if format == "" {
			switch strings.ToLower(filepath.Ext(p)) {
			case ".json":
				format = "json"
			case ".css":
				format = "css"
			}
		}
	}
	if len(src) > maxTokensBytes {
		return "", fmt.Errorf("tokens source is over %d MB", maxTokensBytes>>20)
	}
	if format == "" {
		format = "css"
		if strings.HasPrefix(strings.TrimSpace(string(src)), "{") {
			format = "json"
		}
	}
	var tokens []importToken
	var warnings []string
	switch format {
	case "css":
		if args.Mode != "" {
			return "", fmt.Errorf("mode applies to json; CSS takes its modes from :root and [data-theme=...] selectors")
		}
		tokens, warnings = parseCSSTokens(string(src))
	case "json":
		var err error
		if tokens, warnings, err = parseJSONTokens(src, args.Mode); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown format %q (use css or json)", args.Format)
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("found no tokens: CSS needs --name: value declarations in :root or [data-theme=...]; JSON needs objects with $value")
	}

	raw, err := b.Call(ctx, args.File, "get_variable_defs", emptyArgs{}, bridge.DefaultTimeout)
	if err != nil {
		return "", err
	}
	var defs varDefs
	if err := json.Unmarshal(raw, &defs); err != nil {
		return "", fmt.Errorf("bad variable payload from plugin: %w", err)
	}
	plan := planImport(defs, tokens, args.Collection)
	plan.Skipped = append(warnings, plan.Skipped...)
	if args.DryRun {
		return plan.summary(true), nil
	}
	if len(plan.Created)+len(plan.Updated) == 0 {
		return plan.summary(false), nil
	}

	// Collection and modes first: the batch needs their ids.
	cols := map[string]varCollection{}
	for _, c := range defs.Collections {
		cols[c.ID] = c
	}
	colID := plan.CollectionID
	if colID == "" {
		raw, err := b.Call(ctx, args.File, "create_variable_collection", map[string]any{"name": plan.Collection}, bridge.DefaultTimeout)
		if err != nil {
			return "", fmt.Errorf("create collection %q: %w", plan.Collection, err)
		}
		var c varCollection
		if err := json.Unmarshal(raw, &c); err != nil {
			return "", fmt.Errorf("bad collection payload from plugin: %w", err)
		}
		colID, cols[c.ID] = c.ID, c
	}
	added := map[[2]string]string{}
	toAdd := plan.ModesToAdd
	plan.ModesToAdd = nil // from here on: the modes that were actually added
	for _, m := range toAdd {
		col := m.CollectionID
		if col == "" {
			col = colID
		}
		raw, err := b.Call(ctx, args.File, "add_variable_mode", map[string]any{"collection_id": col, "name": m.Name}, bridge.DefaultTimeout)
		if err != nil {
			plan.dropMode(m.CollectionID, m.Name, err.Error())
			continue
		}
		var r struct {
			ModeID string `json:"modeId"`
		}
		json.Unmarshal(raw, &r)
		added[[2]string{col, m.Name}] = r.ModeID
		plan.ModesToAdd = append(plan.ModesToAdd, m)
	}
	modeIDs := func(col, name string) string {
		if id := added[[2]string{col, name}]; id != "" {
			return id
		}
		return modeID(cols[col], name)
	}
	if len(plan.Created)+len(plan.Updated) == 0 {
		return plan.summary(false), nil
	}
	ops, skipped := plan.batchOps(colID, modeIDs)
	plan.Skipped = append(plan.Skipped, skipped...)

	var failures []string
	if len(ops) > 0 {
		raw, err := b.Call(ctx, args.File, "batch", map[string]any{"ops": ops}, downloadTimeout(len(ops)/20))
		if err != nil {
			return "", err
		}
		var res struct {
			Results []struct {
				Error string `json:"error"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return "", fmt.Errorf("bad batch payload from plugin: %w", err)
		}
		for i, r := range res.Results {
			if r.Error != "" {
				p, _ := ops[i]["params"].(map[string]any)
				what := p["name"]
				if what == nil {
					what = p["variable_id"]
				}
				failures = append(failures, fmt.Sprintf("%s %v: %s", ops[i]["command"], what, r.Error))
			}
		}
	}
	out := plan.summary(false)
	if len(failures) > 0 {
		if len(failures) > 10 {
			failures = append(failures[:10:10], fmt.Sprintf("… and %d more", len(failures)-10))
		}
		out += fmt.Sprintf("%d writes failed:\n  %s\n", len(failures), strings.Join(failures, "\n  "))
	}
	return out, nil
}
