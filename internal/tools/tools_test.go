package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func TestIsSVG(t *testing.T) {
	for in, want := range map[string]bool{
		`<svg xmlns="http://www.w3.org/2000/svg"/>`:                 true,
		"\xef\xbb\xbf  <?xml version=\"1.0\"?>\n<!-- x -->\n<SVG/>": true,
		"\x89PNG\r\n\x1a\n....<svg":                                 false, // a PNG that happens to contain the bytes
		"\xff\xd8\xff\xe0 JFIF":                                     false,
		"<html><body>not found</body></html>":                       false,
	} {
		if got := isSVG([]byte(in)); got != want {
			t.Errorf("isSVG(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for in, want := range map[string]bool{
		"8.8.8.8": true, "104.16.0.1": true, "2606:4700::1": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.10": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "0.1.2.3": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "224.0.0.1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", in, got, want)
		}
	}
}

// The check runs where the connection is made, so an https server on this
// machine is refused even though the URL itself looks fine.
func TestLoadImageRefusesLocalAndHTTP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<svg/>"))
	}))
	defer srv.Close()
	if _, err := loadImageBytes(context.Background(), srv.URL+"/icon.svg"); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("local https: want refusal, got %v", err)
	}
	if _, err := loadImageBytes(context.Background(), "http://example.com/a.png"); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("http: want refusal, got %v", err)
	}
}

// A symlink inside the project must not lead reads or writes out of it.
func TestResolveProjectPathRejectsSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	project := t.TempDir()
	t.Chdir(project)
	if err := os.Symlink(outside, filepath.Join(project, "assets")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	os.MkdirAll(filepath.Join(project, "real", "inner"), 0o755)
	os.Symlink(filepath.Join(project, "real"), filepath.Join(project, "alias"))
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(project, "link.txt")) // dangling file link

	for _, p := range []string{"assets/icon.svg", "assets/new/deep/icon.svg", "link.txt"} {
		if _, err := resolveProjectPath(p); err == nil {
			t.Errorf("%s leads outside the project and must be rejected", p)
		}
	}
	for _, p := range []string{"alias/inner/x.svg", "not/yet/there.svg", "real/inner/x.svg"} {
		if _, err := resolveProjectPath(p); err != nil {
			t.Errorf("%s stays inside the project: %v", p, err)
		}
	}
}
