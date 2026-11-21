package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiffDesign(t *testing.T) {
	before := `{"id":"1:1","name":"Screen","type":"FRAME","x":0,"y":0,"width":100,"height":200,"visible":true,
		"fills":[{"type":"SOLID","visible":true,"opacity":1,"color":{"r":1,"g":1,"b":1}}],
		"children":[
			{"id":"1:2","name":"Title","type":"TEXT","x":0,"y":0,"width":50,"height":20.04,"visible":true,"text":"Hello","fontSize":14,
			 "fontName":{"family":"Inter","style":"Regular"}},
			{"id":"1:3","name":"Old","type":"RECTANGLE","x":0,"y":30,"width":10,"height":10,"visible":true},
			{"id":"1:5","name":"Btn","type":"INSTANCE","x":0,"y":50,"width":80,"height":40,"visible":true,"variant":{"State":"Default"}}
		]}`
	after := `{"id":"1:1","name":"Screen","type":"FRAME","x":0,"y":0,"width":100,"height":200,"visible":true,
		"fills":[{"type":"SOLID","visible":true,"opacity":1,"color":{"r":1,"g":1,"b":1}}],
		"children":[
			{"id":"1:2","name":"Title","type":"TEXT","x":0,"y":0,"width":50,"height":20.01,"visible":true,"text":"Hello world","fontSize":16,
			 "fontName":{"family":"Inter","style":"Bold"}},
			{"id":"1:4","name":"Error","type":"TEXT","x":0,"y":30,"width":10,"height":10,"visible":false,"text":"Required"},
			{"id":"1:5","name":"Btn","type":"INSTANCE","x":0,"y":50,"width":80,"height":40,"visible":true,"variant":{"State":"Disabled"},"dashPattern":[4,2]}
		]}`

	got, err := diffDesign([]byte(before), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"4 nodes before, 4 now: 1 added, 1 removed, 2 changed.",
		`+ TEXT "Error" (1:4) in "Screen": text "Required"`,
		`- RECTANGLE "Old" (1:3) in "Screen"`,
		`fontSize 14 → 16`,
		`font Inter Regular → Inter Bold`,
		`text "Hello" → "Hello world"`,
		`state State=Default → State=Disabled`,
		`dash (none) → 4 2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// 20.04 vs 20.01 rounds to the same 0.1 px: not a change.
	if strings.Contains(got, "height") {
		t.Errorf("sub-pixel height noise reported:\n%s", got)
	}

	same, err := diffDesign([]byte(before), []byte(before))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(same, "No design changes.") {
		t.Errorf("identical trees: got\n%s", same)
	}

	if _, err := diffDesign([]byte("not json"), []byte(after)); err == nil {
		t.Error("want an error for a corrupt baseline")
	}
}

// Showing an icon in an auto layout row: the icon's stale position and the
// fill-width text that shrinks to make room are consequences, not edits.
func TestDiffDesignSkipsLayoutConsequences(t *testing.T) {
	row := func(eyeVisible bool, eyeX, codeW, btnW, badgeX float64) string {
		return fmt.Sprintf(`{"id":"1:1","name":"Row","type":"FRAME","layoutMode":"HORIZONTAL","width":358,"height":44,"children":[
			{"id":"1:2","name":"code","type":"TEXT","x":16,"y":11,"width":%g,"height":21,"text":"abc","layoutSizingHorizontal":"FILL"},
			{"id":"1:3","name":"eye-off","type":"INSTANCE","x":%g,"y":12,"width":20,"height":20,"visible":%t},
			{"id":"1:4","name":"btn","type":"FRAME","x":0,"y":0,"width":%g,"height":20},
			{"id":"1:5","name":"badge","type":"FRAME","x":%g,"y":0,"width":8,"height":8,"layoutPositioning":"ABSOLUTE"}]}`,
			codeW, eyeX, eyeVisible, btnW, badgeX)
	}
	_, _, changes, err := diffChanges([]byte(row(false, 279, 326, 40, 300)), []byte(row(true, 322, 296, 60, 310)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.Name] = c.Detail
	}
	want := map[string]string{
		"eye-off": "shown",         // no stale x
		"btn":     "width 40 → 60", // fixed width: the designer set it
		"badge":   "x 300 → 310",   // absolute: placed by hand
	}
	if len(got) != len(want) {
		t.Errorf("changes %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}
