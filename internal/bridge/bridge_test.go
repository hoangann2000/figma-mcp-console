package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// startBridge serves b on an httptest server and returns the server URL
// (http://127.0.0.1:port).
func startBridge(t *testing.T, b *Bridge) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handlePlugin)
	mux.HandleFunc("/peer", b.handlePeer)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func wsURL(base string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/ws"
}

func srvPort(t *testing.T, base string) int {
	t.Helper()
	i := strings.LastIndex(base, ":")
	port, err := strconv.Atoi(base[i+1:])
	if err != nil {
		t.Fatalf("bad httptest URL %q: %v", base, err)
	}
	return port
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakePlugin dials the bridge, registers as file name, and answers every
// request via reply. It waits until the bridge lists the file.
func fakePlugin(t *testing.T, b *Bridge, base, name string, reply func(Request) *Response) *websocket.Conn {
	t.Helper()
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, wsURL(base), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	if name != "" {
		if err := wsjson.Write(ctx, c, map[string]any{"register": FileInfo{Name: name}}); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	go func() {
		for {
			var req Request
			if err := wsjson.Read(ctx, c, &req); err != nil {
				return
			}
			if req.ID == "" || req.Command == "" {
				continue // hello frame
			}
			if resp := reply(req); resp != nil {
				wsjson.Write(ctx, c, resp)
			}
		}
	}()
	waitUntil(t, "plugin "+name+" to attach", func() bool {
		for _, f := range b.Files() {
			if f.Name == name || name == "" {
				return true
			}
		}
		return name == "" && len(b.Files()) > 0
	})
	return c
}

// echoName replies with the plugin's registered name so tests can verify
// which plugin a call was routed to.
func echoName(name string) func(Request) *Response {
	return func(req Request) *Response {
		out, _ := json.Marshal(map[string]string{"plugin": name})
		return &Response{ID: req.ID, Result: out}
	}
}

func routedTo(t *testing.T, call func() (json.RawMessage, error)) string {
	t.Helper()
	raw, err := call()
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bad result %s: %v", raw, err)
	}
	return m["plugin"]
}

func TestCallRoundTrip(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	fakePlugin(t, b, base, "Solo File", func(req Request) *Response {
		return &Response{ID: req.ID, Result: req.Params}
	})

	got, err := b.CallFile(context.Background(), "", "echo", map[string]string{"hello": "figma"}, DefaultTimeout)
	if err != nil {
		t.Fatalf("CallFile: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil || m["hello"] != "figma" {
		t.Fatalf("round trip mismatch: %s (err %v)", got, err)
	}
}

func TestRoutingByFile(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	fakePlugin(t, b, base, "Landing Page", echoName("Landing Page"))
	fakePlugin(t, b, base, "Design System", echoName("Design System"))
	ctx := context.Background()

	// Exact name, case-insensitive.
	if got := routedTo(t, func() (json.RawMessage, error) { return b.CallFile(ctx, "landing page", "ping", nil, DefaultTimeout) }); got != "Landing Page" {
		t.Fatalf("exact match routed to %q", got)
	}
	// Unique substring.
	if got := routedTo(t, func() (json.RawMessage, error) { return b.CallFile(ctx, "system", "ping", nil, DefaultTimeout) }); got != "Design System" {
		t.Fatalf("substring match routed to %q", got)
	}
	// No file with several connected → error listing the files.
	_, err := b.CallFile(ctx, "", "ping", nil, DefaultTimeout)
	if err == nil || !strings.Contains(err.Error(), "Landing Page") || !strings.Contains(err.Error(), "file parameter") {
		t.Fatalf("want ambiguity error listing files, got %v", err)
	}
	// No match.
	_, err = b.CallFile(ctx, "nonexistent", "ping", nil, DefaultTimeout)
	if err == nil || !strings.Contains(err.Error(), "no connected Figma file matches") {
		t.Fatalf("want no-match error, got %v", err)
	}
	// Ambiguous substring.
	_, err = b.CallFile(ctx, "n", "ping", nil, DefaultTimeout)
	if err == nil || !strings.Contains(err.Error(), "matches several") {
		t.Fatalf("want ambiguous error, got %v", err)
	}

	files := b.Files()
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %v", files)
	}
}

func TestPeerForwarding(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	fakePlugin(t, b, base, "Landing Page", echoName("Landing Page"))
	fakePlugin(t, b, base, "Design System", echoName("Design System"))

	p, err := DialPeer(srvPort(t, base))
	if err != nil {
		t.Fatalf("DialPeer: %v", err)
	}
	ctx := context.Background()

	if got := routedTo(t, func() (json.RawMessage, error) { return p.Call(ctx, "landing", "ping", nil, DefaultTimeout) }); got != "Landing Page" {
		t.Fatalf("peer call routed to %q", got)
	}
	files, err := p.Files(ctx)
	if err != nil || len(files) != 2 {
		t.Fatalf("peer Files: %v, %v", files, err)
	}
	// Errors cross the peer boundary intact.
	_, err = p.Call(ctx, "nonexistent", "ping", nil, DefaultTimeout)
	if err == nil || !strings.Contains(err.Error(), "no connected Figma file matches") {
		t.Fatalf("want routed error via peer, got %v", err)
	}
}

func TestCallPluginError(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	fakePlugin(t, b, base, "F", func(req Request) *Response {
		return &Response{ID: req.ID, Error: "node not found: 1:23"}
	})

	_, err := b.CallFile(context.Background(), "", "get_design_context", map[string]string{"node_id": "1:23"}, DefaultTimeout)
	if err == nil || !strings.Contains(err.Error(), "node not found") {
		t.Fatalf("want plugin error, got %v", err)
	}
}

func TestCallNotConnected(t *testing.T) {
	b := New()
	_, err := b.CallFile(context.Background(), "", "get_metadata", nil, DefaultTimeout)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestCallTimeout(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	fakePlugin(t, b, base, "Slow", func(req Request) *Response { return nil }) // never answers

	_, err := b.CallFile(context.Background(), "", "slow", nil, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("want timeout error, got %v", err)
	}
	b.mu.Lock()
	n := len(b.pending)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("pending map leaked %d entries", n)
	}
}

func TestDisconnectFailsPending(t *testing.T) {
	b := New()
	base := startBridge(t, b)
	c := fakePlugin(t, b, base, "Doomed", func(req Request) *Response { return nil })

	done := make(chan error, 1)
	go func() {
		_, err := b.CallFile(context.Background(), "", "hang", nil, 5*time.Second)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the call register + send
	c.CloseNow()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("want disconnected error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not return after plugin disconnect")
	}
	waitUntil(t, "plugin to be dropped", func() bool { return len(b.Files()) == 0 })
}

// TestRouterElection verifies that of two sessions on one port, the first
// becomes the owner, the second joins as a peer, and both can call plugins.
func TestRouterElection(t *testing.T) {
	const port = 29471
	r1 := NewRouter("proj-one", port)
	go r1.Run()
	waitUntil(t, "r1 to own the bridge", func() bool { o, _ := r1.roles(); return o != nil })
	owner, _ := r1.roles()

	r2 := NewRouter("proj-two", port)
	go r2.Run()
	waitUntil(t, "r2 to join as peer", func() bool { _, p := r2.roles(); return p != nil })

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	fakePlugin(t, owner, base, "Landing Page", echoName("Landing Page"))
	fakePlugin(t, owner, base, "Design System", echoName("Design System"))
	ctx := context.Background()

	if got := routedTo(t, func() (json.RawMessage, error) { return r1.Call(ctx, "landing", "ping", nil, DefaultTimeout) }); got != "Landing Page" {
		t.Fatalf("owner routed to %q", got)
	}
	if got := routedTo(t, func() (json.RawMessage, error) { return r2.Call(ctx, "design s", "ping", nil, DefaultTimeout) }); got != "Design System" {
		t.Fatalf("peer routed to %q", got)
	}
	files, err := r2.Files(ctx)
	if err != nil || len(files) != 2 {
		t.Fatalf("peer Files: %v, %v", files, err)
	}
}
