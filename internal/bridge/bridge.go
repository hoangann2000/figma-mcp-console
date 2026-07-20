// Package bridge implements the local WebSocket bridge between MCP server
// sessions and the Figma plugin windows running inside Figma Desktop.
//
// Exactly one bridge owner exists per machine: the first MCP session to bind
// the bridge port. It accepts every plugin connection (one per open Figma
// file, on /ws) and every other MCP session (peers, on /peer), and routes
// each tool call to the right file's plugin. See Router for the election.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// ErrNotConnected is returned by calls when no Figma plugin is connected.
var ErrNotConnected = errors.New(
	"no Figma plugin is connected. In Figma Desktop, open your design file, " +
		"run Plugins → Development → Figma MCP Console, and keep its window open. " +
		"Run it in every file you want to work with")

// Request is sent from the bridge owner to a Figma plugin.
type Request struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is sent from a Figma plugin back to the bridge owner.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// FileInfo identifies one connected Figma file (one plugin window).
type FileInfo struct {
	Key  string `json:"key,omitempty"`
	Name string `json:"name"`
}

// pluginFrame is anything a plugin may send: a command response, or a
// register frame announcing which file the plugin runs in.
type pluginFrame struct {
	ID       string          `json:"id"`
	Result   json.RawMessage `json:"result"`
	Error    string          `json:"error"`
	Register *FileInfo       `json:"register"`
}

// maxMessageSize accommodates large base64 screenshot payloads; the
// coder/websocket default of 32 KiB would kill the connection.
const maxMessageSize = 50 << 20

// DefaultTimeout applies to ordinary tool calls; slow operations like
// screenshot export pass a larger timeout.
const DefaultTimeout = 15 * time.Second

// maxPeerTimeout bounds the timeout a peer may request from the owner.
const maxPeerTimeout = 10 * time.Minute

// cmdListFiles is the peer-protocol pseudo-command answered by the owner
// itself instead of being forwarded to a plugin.
const cmdListFiles = "_list_files"

// pluginConn is one attached plugin window.
type pluginConn struct {
	id      uint64
	c       *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	info    FileInfo
}

func (pc *pluginConn) file() FileInfo {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.info
}

func (pc *pluginConn) write(ctx context.Context, v any) error {
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	return wsjson.Write(ctx, pc.c, v)
}

// label names the connection in logs and errors even before registration.
func (pc *pluginConn) label() string {
	if fi := pc.file(); fi.Name != "" {
		return fi.Name
	}
	return fmt.Sprintf("unregistered plugin #%d", pc.id)
}

type pendingCall struct {
	ch     chan Response
	connID uint64
}

// Bridge is the owner side: it serves plugin and peer connections and
// correlates plugin responses back to pending calls.
type Bridge struct {
	mu       sync.Mutex
	conns    map[uint64]*pluginConn
	pending  map[string]pendingCall
	nextConn atomic.Uint64
	nextID   atomic.Uint64
	project  string
	port     int
}

func New() *Bridge {
	return &Bridge{
		conns:   make(map[uint64]*pluginConn),
		pending: make(map[string]pendingCall),
	}
}

// SetProject names the project of the owning session; it is shown in the
// plugin UI status line.
func (b *Bridge) SetProject(name string) { b.project = name }

// listen binds the bridge port on loopback. Both IPv4 (127.0.0.1) and IPv6
// (::1) are bound: the plugin connects to ws://localhost, and on macOS
// /etc/hosts maps localhost to both. Chromium inside Figma Desktop may pick
// ::1 first and not fall back, so an IPv4-only server would leave the plugin
// stuck searching. The IPv4 bind decides whether the port is free; IPv6 is
// best-effort.
func (b *Bridge) listen(port int) ([]net.Listener, error) {
	v4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	lns := []net.Listener{v4}
	if v6, err6 := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err6 == nil {
		lns = append(lns, v6)
	} else {
		log.Printf("bridge: IPv6 loopback bind on port %d skipped: %v", port, err6)
	}
	b.port = port
	return lns, nil
}

// serve runs the WebSocket endpoints on the bound listeners and blocks until
// one fails, then tears everything down so a re-election can start clean.
func (b *Bridge) serve(lns []net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", b.handlePlugin)
	mux.HandleFunc("/peer", b.handlePeer)
	errCh := make(chan error, len(lns))
	for _, ln := range lns {
		go func(l net.Listener) { errCh <- http.Serve(l, mux) }(ln)
	}
	err := <-errCh
	for _, ln := range lns {
		ln.Close()
	}
	b.closeAll()
	return err
}

func (b *Bridge) closeAll() {
	b.mu.Lock()
	conns := make([]*pluginConn, 0, len(b.conns))
	for _, pc := range b.conns {
		conns = append(conns, pc)
	}
	b.conns = make(map[uint64]*pluginConn)
	for id, p := range b.pending {
		p.ch <- Response{ID: id, Error: "bridge shut down"}
		delete(b.pending, id)
	}
	b.mu.Unlock()
	for _, pc := range conns {
		pc.c.CloseNow()
	}
}

func (b *Bridge) handlePlugin(w http.ResponseWriter, r *http.Request) {
	// The plugin iframe has a null/opaque origin, so origin verification must
	// be skipped; this is safe because we only bind to loopback.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Printf("bridge: plugin accept failed: %v", err)
		return
	}
	c.SetReadLimit(maxMessageSize)

	pc := &pluginConn{id: b.nextConn.Add(1), c: c}
	b.mu.Lock()
	b.conns[pc.id] = pc
	n := len(b.conns)
	b.mu.Unlock()
	log.Printf("bridge: figma plugin connected (%d attached)", n)

	// The hello frame is the adoption handshake and feeds the plugin UI
	// status line; the plugin answers with a register frame naming its file.
	_ = pc.write(context.Background(), map[string]any{
		"hello": map[string]any{"project": b.project, "port": b.port},
	})

	b.readPlugin(pc)
}

// readPlugin routes one plugin's frames until the connection dies.
func (b *Bridge) readPlugin(pc *pluginConn) {
	ctx := context.Background()
	for {
		var f pluginFrame
		if err := wsjson.Read(ctx, pc.c, &f); err != nil {
			b.dropPlugin(pc, err)
			return
		}
		if f.Register != nil {
			pc.mu.Lock()
			pc.info = *f.Register
			pc.mu.Unlock()
			log.Printf("bridge: plugin registered file %q", f.Register.Name)
			continue
		}
		if f.ID == "" {
			continue
		}
		b.mu.Lock()
		p, ok := b.pending[f.ID]
		delete(b.pending, f.ID)
		b.mu.Unlock()
		if ok {
			p.ch <- Response{ID: f.ID, Result: f.Result, Error: f.Error}
		}
	}
}

func (b *Bridge) dropPlugin(pc *pluginConn, err error) {
	b.mu.Lock()
	delete(b.conns, pc.id)
	for id, p := range b.pending {
		if p.connID == pc.id {
			p.ch <- Response{ID: id, Error: "plugin disconnected"}
			delete(b.pending, id)
		}
	}
	n := len(b.conns)
	b.mu.Unlock()
	pc.c.CloseNow()
	log.Printf("bridge: figma plugin %q disconnected (%d attached): %v", pc.label(), n, err)
}

// snapshot returns the attached plugins in connection order.
func (b *Bridge) snapshot() []*pluginConn {
	b.mu.Lock()
	conns := make([]*pluginConn, 0, len(b.conns))
	for _, pc := range b.conns {
		conns = append(conns, pc)
	}
	b.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool { return conns[i].id < conns[j].id })
	return conns
}

// Files lists the connected Figma files.
func (b *Bridge) Files() []FileInfo {
	conns := b.snapshot()
	out := make([]FileInfo, 0, len(conns))
	for _, pc := range conns {
		fi := pc.file()
		if fi.Name == "" {
			fi.Name = pc.label()
		}
		out = append(out, fi)
	}
	return out
}

func nameList(conns []*pluginConn) string {
	names := make([]string, len(conns))
	for i, pc := range conns {
		names[i] = fmt.Sprintf("%q", pc.label())
	}
	return strings.Join(names, ", ")
}

// pick chooses the plugin for a call. An empty file is allowed only while a
// single plugin is attached; otherwise file must match one connected file's
// key or name (case-insensitive; a unique name substring also works).
func (b *Bridge) pick(file string) (*pluginConn, error) {
	conns := b.snapshot()
	if len(conns) == 0 {
		return nil, ErrNotConnected
	}
	if strings.TrimSpace(file) == "" {
		if len(conns) == 1 {
			return conns[0], nil
		}
		return nil, fmt.Errorf(
			"%d Figma files are connected (%s); pass the file parameter to choose one",
			len(conns), nameList(conns))
	}
	want := strings.ToLower(strings.TrimSpace(file))
	var exact, partial []*pluginConn
	for _, pc := range conns {
		fi := pc.file()
		key, name := strings.ToLower(fi.Key), strings.ToLower(fi.Name)
		switch {
		case (key != "" && key == want) || (name != "" && name == want):
			exact = append(exact, pc)
		case name != "" && strings.Contains(name, want):
			partial = append(partial, pc)
		}
	}
	m := exact
	if len(m) == 0 {
		m = partial
	}
	switch len(m) {
	case 1:
		return m[0], nil
	case 0:
		return nil, fmt.Errorf("no connected Figma file matches %q; connected: %s",
			file, nameList(conns))
	default:
		return nil, fmt.Errorf("file %q matches several connected files (%s); be more specific",
			file, nameList(m))
	}
}

// CallFile sends a command to the plugin of the given file (see pick) and
// waits for the correlated response.
func (b *Bridge) CallFile(ctx context.Context, file, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	pc, err := b.pick(file)
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("%d", b.nextID.Add(1))
	ch := make(chan Response, 1)
	b.mu.Lock()
	b.pending[id] = pendingCall{ch: ch, connID: pc.id}
	b.mu.Unlock()
	unregister := func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}

	if err := pc.write(ctx, Request{ID: id, Command: command, Params: raw}); err != nil {
		unregister()
		return nil, fmt.Errorf("send to plugin %q: %w", pc.label(), err)
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
		return nil, fmt.Errorf("figma plugin %q did not answer %q within %s", pc.label(), command, timeout)
	}
}

// peerRequest is one forwarded tool call from a peer MCP session.
type peerRequest struct {
	ID        string          `json:"id"`
	File      string          `json:"file,omitempty"`
	Command   string          `json:"command"`
	Params    json.RawMessage `json:"params,omitempty"`
	TimeoutMs int64           `json:"timeout_ms,omitempty"`
}

type peerResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// handlePeer serves one peer MCP session: every request is handled in its
// own goroutine so a slow screenshot doesn't block the peer's other calls.
func (b *Bridge) handlePeer(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Printf("bridge: peer accept failed: %v", err)
		return
	}
	c.SetReadLimit(maxMessageSize)
	log.Print("bridge: peer session connected")

	ctx := context.Background()
	var writeMu sync.Mutex
	for {
		var req peerRequest
		if err := wsjson.Read(ctx, c, &req); err != nil {
			log.Printf("bridge: peer session disconnected: %v", err)
			c.CloseNow()
			return
		}
		go func(req peerRequest) {
			timeout := DefaultTimeout
			if req.TimeoutMs > 0 {
				timeout = time.Duration(req.TimeoutMs) * time.Millisecond
				if timeout > maxPeerTimeout {
					timeout = maxPeerTimeout
				}
			}
			resp := peerResponse{ID: req.ID}
			if req.Command == cmdListFiles {
				resp.Result, _ = json.Marshal(b.Files())
			} else if raw, err := b.CallFile(ctx, req.File, req.Command, req.Params, timeout); err != nil {
				resp.Error = err.Error()
			} else {
				resp.Result = raw
			}
			writeMu.Lock()
			defer writeMu.Unlock()
			_ = wsjson.Write(ctx, c, resp)
		}(req)
	}
}
