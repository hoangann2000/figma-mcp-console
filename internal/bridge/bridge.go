// Package bridge implements the local WebSocket bridge between the MCP
// server and the Figma plugin running inside Figma Desktop.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// ErrNotConnected is returned by Call when no Figma plugin is connected.
var ErrNotConnected = errors.New(
	"Figma plugin is not connected. In Figma Desktop, open your design file, " +
		"run Plugins → Development → Figma MCP Console, and keep its window open")

// Request is sent from the Go server to the Figma plugin.
type Request struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is sent from the Figma plugin back to the Go server.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// maxMessageSize accommodates large base64 screenshot payloads; the
// coder/websocket default of 32 KiB would kill the connection.
const maxMessageSize = 50 << 20

// DefaultTimeout applies to ordinary tool calls; slow operations like
// screenshot export pass a larger timeout via CallTimeout.
const DefaultTimeout = 15 * time.Second

// Bridge owns the WebSocket server and correlates plugin responses back to
// pending Call invocations. At most one plugin connection is active; a new
// connection replaces the old one (plugin restart).
type Bridge struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
	pending map[string]chan Response
	nextID  atomic.Uint64
}

func New() *Bridge {
	return &Bridge{pending: make(map[string]chan Response)}
}

// Serve listens on addr (e.g. "127.0.0.1:2000") and accepts plugin
// connections on /ws. It blocks until the listener fails.
func (b *Bridge) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handleWS)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bridge listen on %s: %w", addr, err)
	}
	log.Printf("bridge: listening on ws://%s/ws", addr)
	return http.Serve(ln, mux)
}

func (b *Bridge) handleWS(w http.ResponseWriter, r *http.Request) {
	// The plugin iframe has a null/opaque origin, so origin verification must
	// be skipped; this is safe because we only bind to localhost.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Printf("bridge: accept failed: %v", err)
		return
	}
	c.SetReadLimit(maxMessageSize)

	b.mu.Lock()
	if b.conn != nil {
		log.Print("bridge: new plugin connection, replacing the old one")
		b.conn.CloseNow()
		b.failPendingLocked("plugin reconnected, request abandoned")
	}
	b.conn = c
	b.mu.Unlock()
	log.Print("bridge: figma plugin connected")

	b.readLoop(c)
}

// readLoop routes plugin responses to their waiting callers until the
// connection dies.
func (b *Bridge) readLoop(c *websocket.Conn) {
	ctx := context.Background()
	for {
		var resp Response
		if err := wsjson.Read(ctx, c, &resp); err != nil {
			b.mu.Lock()
			if b.conn == c {
				b.conn = nil
				b.failPendingLocked("plugin disconnected")
				log.Printf("bridge: figma plugin disconnected: %v", err)
			}
			b.mu.Unlock()
			c.CloseNow()
			return
		}
		b.mu.Lock()
		ch, ok := b.pending[resp.ID]
		delete(b.pending, resp.ID)
		b.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

func (b *Bridge) failPendingLocked(reason string) {
	for id, ch := range b.pending {
		ch <- Response{ID: id, Error: reason}
		delete(b.pending, id)
	}
}

// Call sends a command to the plugin and waits for the correlated response.
func (b *Bridge) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	return b.CallTimeout(ctx, command, params, DefaultTimeout)
}

func (b *Bridge) CallTimeout(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	b.mu.Lock()
	conn := b.conn
	if conn == nil {
		b.mu.Unlock()
		return nil, ErrNotConnected
	}
	id := fmt.Sprintf("%d", b.nextID.Add(1))
	ch := make(chan Response, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	unregister := func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}

	b.writeMu.Lock()
	err = wsjson.Write(ctx, conn, Request{ID: id, Command: command, Params: raw})
	b.writeMu.Unlock()
	if err != nil {
		unregister()
		return nil, fmt.Errorf("send to plugin: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		unregister()
		return nil, ctx.Err()
	case <-timer.C:
		unregister()
		return nil, fmt.Errorf("figma plugin did not answer %q within %s", command, timeout)
	}
}

// Connected reports whether a plugin is currently attached.
func (b *Bridge) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil
}
