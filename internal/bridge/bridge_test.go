package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// startBridge serves b on an httptest server and returns the ws:// URL.
func startBridge(t *testing.T, b *Bridge) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// fakePlugin dials the bridge and answers every request via reply.
func fakePlugin(t *testing.T, url string, reply func(Request) *Response) {
	t.Helper()
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		for {
			var req Request
			if err := wsjson.Read(ctx, c, &req); err != nil {
				return
			}
			if resp := reply(req); resp != nil {
				wsjson.Write(ctx, c, resp)
			}
		}
	}()
	// Give the bridge a moment to register the connection.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pluginConnected(url) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var testBridges = map[string]*Bridge{}

func pluginConnected(url string) bool {
	if b, ok := testBridges[url]; ok {
		return b.Connected()
	}
	return true
}

func TestCallRoundTrip(t *testing.T) {
	b := New()
	url := startBridge(t, b)
	testBridges[url] = b
	fakePlugin(t, url, func(req Request) *Response {
		return &Response{ID: req.ID, Result: req.Params}
	})

	got, err := b.Call(context.Background(), "echo", map[string]string{"hello": "figma"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil || m["hello"] != "figma" {
		t.Fatalf("round trip mismatch: %s (err %v)", got, err)
	}
}

func TestCallPluginError(t *testing.T) {
	b := New()
	url := startBridge(t, b)
	testBridges[url] = b
	fakePlugin(t, url, func(req Request) *Response {
		return &Response{ID: req.ID, Error: "node not found: 1:23"}
	})

	_, err := b.Call(context.Background(), "get_design_context", map[string]string{"node_id": "1:23"})
	if err == nil || !strings.Contains(err.Error(), "node not found") {
		t.Fatalf("want plugin error, got %v", err)
	}
}

func TestCallNotConnected(t *testing.T) {
	b := New()
	_, err := b.Call(context.Background(), "get_metadata", nil)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestCallTimeout(t *testing.T) {
	b := New()
	url := startBridge(t, b)
	testBridges[url] = b
	fakePlugin(t, url, func(req Request) *Response { return nil }) // never answers

	_, err := b.CallTimeout(context.Background(), "slow", nil, 100*time.Millisecond)
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
	url := startBridge(t, b)
	testBridges[url] = b

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !b.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() {
		_, err := b.CallTimeout(ctx, "hang", nil, 5*time.Second)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let Call register + send
	c.CloseNow()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("want disconnected error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Call did not return after plugin disconnect")
	}
	if b.Connected() {
		t.Fatal("bridge still reports connected after disconnect")
	}
}
