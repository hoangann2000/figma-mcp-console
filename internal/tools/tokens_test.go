package tools

import (
	"strings"
	"testing"
)

const tokenDefs = `{
 "collections":[{"id":"C1","name":"theme","defaultModeId":"m1","modes":[{"modeId":"m1","name":"Light"},{"modeId":"m2","name":"Dark Mode"}]}],
 "variables":[
  {"id":"V1","name":"color/bg/Primary","resolvedType":"COLOR","collectionId":"C1","valuesByMode":{"m1":{"r":1,"g":1,"b":1,"a":1},"m2":{"r":0,"g":0,"b":0,"a":0.5}}},
  {"id":"V2","name":"color/surface","resolvedType":"COLOR","collectionId":"C1","valuesByMode":{"m1":{"type":"VARIABLE_ALIAS","id":"V1"},"m2":{"type":"VARIABLE_ALIAS","id":"LIB:9"}}},
  {"id":"V3","name":"space/md","resolvedType":"FLOAT","collectionId":"C1","valuesByMode":{"m1":16,"m2":16}},
  {"id":"V4","name":"font/body","resolvedType":"STRING","collectionId":"C1","valuesByMode":{"m1":"Inter","m2":"Inter"}}
 ]}`

func TestRenderTokensCSS(t *testing.T) {
	got, n, err := renderTokens([]byte(tokenDefs), "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := `:root {
  --color-bg-primary: #ffffff;
  --color-surface: var(--color-bg-primary);
  --space-md: 16;
  --font-body: "Inter";
}

[data-theme="dark-mode"] {
  --color-bg-primary: rgba(0, 0, 0, 0.5);
  --color-surface: var(--lib-9);
  --space-md: 16;
  --font-body: "Inter";
}
`
	if got != want || n != 8 {
		t.Errorf("n=%d, css:\n%s\nwant:\n%s", n, got, want)
	}
	// One mode on request lands under :root, whichever mode it is.
	dark, n, err := renderTokens([]byte(tokenDefs), "css", "dark mode")
	if err != nil || n != 4 || !strings.HasPrefix(dark, ":root {\n  --color-bg-primary: rgba(0, 0, 0, 0.5);") {
		t.Errorf("mode filter: n=%d err=%v\n%s", n, err, dark)
	}
}

func TestRenderTokensJSON(t *testing.T) {
	got, n, err := renderTokens([]byte(tokenDefs), "json", "")
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, want := range []string{
		`"$value": "#ffffff"`,            // default mode only
		`"$value": "{color.bg.Primary}"`, // alias kept as a reference
		`"$type": "number"`,
		`"$value": 16`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("json lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "rgba") {
		t.Errorf("json leaked a non-default mode:\n%s", got)
	}
}

func TestRenderTokensErrors(t *testing.T) {
	if _, _, err := renderTokens([]byte(tokenDefs), "css", "sepia"); err == nil || !strings.Contains(err.Error(), "Light, Dark Mode") {
		t.Errorf("unknown mode should list the real ones, got %v", err)
	}
	if _, _, err := renderTokens([]byte(tokenDefs), "scss", ""); err == nil {
		t.Error("unknown format should fail")
	}
	if _, _, err := renderTokens([]byte(`{"collections":[],"variables":[]}`), "", ""); err == nil {
		t.Error("a file without variables should say so")
	}
}
