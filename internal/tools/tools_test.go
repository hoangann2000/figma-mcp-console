package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssetFileName(t *testing.T) {
	cases := map[string]string{
		"icon/Arrow Right":  "arrow-right",       // prefix stripped, spaces → dash
		"Background_Simple": "background-simple", // underscore → dash
		"img/Hero Banner":   "hero-banner",
		"Name/With/Slashes": "slashes", // only the part after the last slash
		"already-kebab":     "already-kebab",
		"  Spaces  ":        "spaces",     // leading/trailing collapse & trim
		"Café Ünïcode":      "caf-n-code", // non-ascii dropped, no double dash
		"":                  "",
		"///":               "",
	}
	for in, want := range cases {
		if got := assetFileName(in); got != want {
			t.Errorf("assetFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveProjectPathAllowsInRoot(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveProjectPath("public/icons/x.svg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(root, "public/icons/x.svg"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A path that dips into .. but stays inside the root is fine.
	if _, err := resolveProjectPath("a/b/../c.svg"); err != nil {
		t.Errorf("in-root path with internal .. should be allowed: %v", err)
	}
}

func TestResolveProjectPathRejectsEscape(t *testing.T) {
	for _, p := range []string{
		"",               // empty
		"../escape.txt",  // parent
		"a/../../escape", // climbs past root after joining
		"/etc/passwd",    // absolute, outside root
	} {
		if _, err := resolveProjectPath(p); err == nil {
			t.Errorf("resolveProjectPath(%q) = nil error, want rejection", p)
		}
	}
}

func TestDownloadTimeout(t *testing.T) {
	if got := downloadTimeout(0); got != screenshotTimeout {
		t.Errorf("downloadTimeout(0) = %v, want %v", got, screenshotTimeout)
	}
	if got, want := downloadTimeout(2), screenshotTimeout+2*5*time.Second; got != want {
		t.Errorf("downloadTimeout(2) = %v, want %v", got, want)
	}
	if got := downloadTimeout(1000); got != 5*time.Minute {
		t.Errorf("downloadTimeout(1000) = %v, want cap 5m", got)
	}
}

// assetFileName output must be safe to drop into a file path: no separators,
// no leading/trailing dashes that would create odd file names.
func TestAssetFileNameIsPathSafe(t *testing.T) {
	for _, in := range []string{"a/b/c", "..", "  ", "Weird//Name", "-x-"} {
		got := assetFileName(in)
		if strings.ContainsAny(got, "/\\") {
			t.Errorf("assetFileName(%q) = %q contains a path separator", in, got)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("assetFileName(%q) = %q has a stray leading/trailing dash", in, got)
		}
	}
}
