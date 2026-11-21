package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseCSSTokens(t *testing.T) {
	toks, warn := parseCSSTokens(`
/* brand */
:root {
  --color-primary: #E11D48;
  --color-overlay: rgba(0, 0, 0, 0.5);
  --color-button: var(--color-primary);
  --space-md: 16px;
  --font-body: "Inter";
  --font-stack: Inter, sans-serif;
  --radius: 1rem;
  --flag: true;
  color: red;
}
[data-theme="dark"] { --color-primary: #fb7185; }
.contrast { --color-primary: #000; }
@media (prefers-color-scheme: dark) { :root { --color-primary: #111; } }
.card > p { --x: 1; }
`)
	got := map[string]importToken{}
	for _, tk := range toks {
		got[tk.Name] = tk
	}
	if len(toks) != 8 {
		t.Fatalf("want 8 tokens, got %d: %+v", len(toks), toks)
	}
	p := got["color-primary"].Values
	if len(p) != 3 || p[""].Type != "COLOR" || p["dark"].Type != "COLOR" || p["contrast"].Type != "COLOR" {
		t.Errorf("modes from :root, [data-theme], .class: %+v", p)
	}
	if c := got["color-overlay"].Values[""].Value.(map[string]float64); c["a"] != 0.5 {
		t.Errorf("rgba alpha: %v", c)
	}
	checks := map[string]importValue{
		"color-button": {Alias: "color-primary"},
		"space-md":     {Type: "FLOAT", Value: 16.0},
		"font-body":    {Type: "STRING", Value: "Inter"},
		"font-stack":   {Type: "STRING", Value: "Inter, sans-serif"},
		"radius":       {Type: "STRING", Value: "1rem"},
		"flag":         {Type: "BOOLEAN", Value: true},
	}
	for name, want := range checks {
		if v := got[name].Values[""]; v != want {
			t.Errorf("%s = %+v, want %+v", name, v, want)
		}
	}
	if len(warn) != 2 || !strings.Contains(warn[0], "@media") || !strings.Contains(warn[1], ".card > p") {
		t.Errorf("skipped blocks must be reported: %q", warn)
	}
}

func TestParseJSONTokens(t *testing.T) {
	toks, warn, err := parseJSONTokens([]byte(`{
 "color": {"primary": {"$type": "color", "$value": "#e11d48"},
           "button":  {"$value": "{color.primary}"}},
 "space": {"md": {"$type": "dimension", "$value": {"value": 16, "unit": "px"}},
           "lg": {"$value": 24}},
 "odd": {"$value": [1, 2]}
}`), "Dark")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]importValue{}
	for _, tk := range toks {
		got[tk.Name] = tk.Values["Dark"]
	}
	if got["color/button"].Alias != "color/primary" || got["space/md"].Value != 16.0 || got["space/lg"].Value != 24.0 || got["color/primary"].Type != "COLOR" {
		t.Errorf("parsed: %+v", got)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "odd") {
		t.Errorf("unsupported value must be reported: %q", warn)
	}
}

func defsFrom(t *testing.T, s string) varDefs {
	t.Helper()
	var d varDefs
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// What export_tokens writes, import_tokens reads back as "nothing to do".
func TestImportRoundTripIsANoOp(t *testing.T) {
	defs := defsFrom(t, tokenDefs)
	for _, format := range []string{"css", "json"} {
		out, _, err := renderTokens([]byte(tokenDefs), format, "")
		if err != nil {
			t.Fatal(err)
		}
		var toks []importToken
		if format == "css" {
			toks, _ = parseCSSTokens(out)
		} else {
			toks, _, _ = parseJSONTokens([]byte(out), "")
		}
		p := planImport(defs, toks, "")
		// The dark alias to a library variable (LIB:9) is not in the file.
		// CSS carries every mode, so its dark alias to a library variable
		// (LIB:9, not in the file) is skipped and that token is not "unchanged".
		wantSkipped, wantUnchanged := map[string]int{"css": 1, "json": 0}[format], map[string]int{"css": 3, "json": 4}[format]
		if len(p.Created)+len(p.Updated) != 0 || p.Unchanged != wantUnchanged || len(p.Skipped) != wantSkipped {
			t.Errorf("%s round trip: created %v updated %v unchanged %d skipped %v", format, p.Created, p.Updated, p.Unchanged, p.Skipped)
		}
	}
}

func TestPlanCreatesUpdatesAndAliasesForward(t *testing.T) {
	defs := defsFrom(t, tokenDefs)
	toks, _ := parseCSSTokens(`
:root {
  --color-link: var(--color-accent);   /* forward alias to a new token */
  --color-accent: #2563eb;              /* new */
  --color-bg-primary: #fefefe;          /* existing, changed */
  --space-md: 16;                       /* existing, unchanged */
  --font-body: #ffffff;                 /* existing STRING given a color */
  --color-ghost: var(--nope);           /* alias to nothing */
}
[data-theme="sepia"] { --color-accent: #a16207; }`)
	p := planImport(defs, toks, "")
	if strings.Join(p.Created, ",") != "color-link,color-accent" || strings.Join(p.Updated, ",") != "color-bg-primary" || p.Unchanged != 1 {
		t.Fatalf("created %v updated %v unchanged %d", p.Created, p.Updated, p.Unchanged)
	}
	if len(p.Skipped) != 2 || !strings.Contains(strings.Join(p.Skipped, "|"), "font-body: value is a COLOR but the variable is a STRING") {
		t.Errorf("skipped: %q", p.Skipped)
	}
	if len(p.ModesToAdd) != 1 || p.ModesToAdd[0] != (planMode{"C1", "sepia"}) {
		t.Errorf("modes to add: %+v", p.ModesToAdd)
	}
	ops, skipped := p.batchOps("C1", func(col, name string) string {
		return map[string]string{"": "m1", "sepia": "m3"}[name]
	})
	if len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	b, _ := json.Marshal(ops)
	s := string(b)
	for _, want := range []string{
		`{"command":"create_variable","params":{"collection_id":"C1","name":"color-link","type":"COLOR"}}`,
		`{"command":"create_variable","params":{"collection_id":"C1","name":"color-accent","type":"COLOR"}}`,
		`"mode_id":"m1","value":{"id":"$1","type":"VARIABLE_ALIAS"},"variable_id":"$0"`, // link -> accent, created later in the same batch
		`"mode_id":"m3","value":{"a":1,"b":0.027450980392156862,"g":0.3843137254901961,"r":0.6313725490196078},"variable_id":"$1"`,
		`"variable_id":"V1"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("batch lacks %s\n%s", want, s)
		}
	}
	if strings.Index(s, "set_variable_value") < strings.LastIndex(s, "create_variable\"") {
		t.Error("every create must come before the first set")
	}
}

type fakeCaller struct {
	calls   []string
	replies map[string]string
	params  map[string]any
}

func (f *fakeCaller) Call(_ context.Context, _, command string, params any, _ time.Duration) (json.RawMessage, error) {
	f.calls = append(f.calls, command)
	if f.params == nil {
		f.params = map[string]any{}
	}
	f.params[command] = params
	return json.RawMessage(f.replies[command]), nil
}

func TestImportTokensCreatesCollectionModesThenOneBatch(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{
		"get_variable_defs":          `{"collections":[],"variables":[]}`,
		"create_variable_collection": `{"id":"C9","name":"Brand","defaultModeId":"d","modes":[{"modeId":"d","name":"Mode 1"}]}`,
		"add_variable_mode":          `{"modeId":"dk"}`,
		"batch":                      `{"ok":true,"results":[{},{},{},{},{}]}`,
	}}
	css := `:root { --a: #fff; --b: var(--a); } [data-theme=dark] { --a: #000; }`
	out, err := importTokens(context.Background(), f, importTokensArgs{Content: css, Collection: "Brand", DryRun: true})
	if err != nil || strings.Join(f.calls, ",") != "get_variable_defs" || !strings.Contains(out, "Dry run") {
		t.Fatalf("dry run must only read: calls %v, out %q, err %v", f.calls, out, err)
	}
	f.calls = nil
	out, err = importTokens(context.Background(), f, importTokensArgs{Content: css, Collection: "Brand"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "get_variable_defs,create_variable_collection,add_variable_mode,batch" {
		t.Errorf("calls %v", f.calls)
	}
	ops := f.params["batch"].(map[string]any)["ops"].([]map[string]any)
	if len(ops) != 5 { // 2 creates + a(default) + a(dark) + b
		t.Errorf("want 5 ops, got %d", len(ops))
	}
	if !strings.Contains(out, "2 created") {
		t.Errorf("summary: %s", out)
	}
	if _, err := importTokens(context.Background(), f, importTokensArgs{Content: css, Mode: "dark"}); err == nil {
		t.Error("mode with CSS input should be refused")
	}
}

type modeLimitCaller struct{ fakeCaller }

func (f *modeLimitCaller) Call(ctx context.Context, file, command string, params any, d time.Duration) (json.RawMessage, error) {
	if command == "add_variable_mode" {
		f.calls = append(f.calls, command)
		return nil, errors.New("in addMode: Limited to 1 modes only")
	}
	return f.fakeCaller.Call(ctx, file, command, params, d)
}

// On a plan limited to one mode, the dark values are skipped and said so, and a
// second run with nothing else to change reports nothing written.
func TestImportTokensModeLimit(t *testing.T) {
	f := &modeLimitCaller{fakeCaller{replies: map[string]string{
		"get_variable_defs": `{"collections":[{"id":"C1","name":"theme","defaultModeId":"m1","modes":[{"modeId":"m1","name":"Light"}]}],
		  "variables":[{"id":"V1","name":"a","resolvedType":"COLOR","collectionId":"C1","valuesByMode":{"m1":{"r":1,"g":1,"b":1,"a":1}}}]}`,
		"batch": `{"ok":true,"results":[]}`,
	}}}
	out, err := importTokens(context.Background(), f, importTokensArgs{Content: `:root{--a:#fff} [data-theme=dark]{--a:#000}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "get_variable_defs,add_variable_mode" {
		t.Errorf("no batch is needed once the dark value is dropped: %v", f.calls)
	}
	if !strings.Contains(out, "0 created, 0 updated, 0 unchanged") || !strings.Contains(out, `mode "dark": 1 values skipped`) || !strings.Contains(out, "Limited to 1 modes") || strings.Contains(out, "Modes added") {
		t.Errorf("summary: %s", out)
	}
}
