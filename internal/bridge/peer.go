package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Peer is the client side of a non-owner MCP session: it forwards tool calls
// over a single WebSocket to the bridge owner, which routes them to the
// right plugin.
type Peer struct {
	c       *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan peerResponse
	nextID  atomic.Uint64
	done    chan struct{}
}

// DialPeer connects to the bridge owner's /peer endpoint on the given port.
func DialPeer(port int) (*Peer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/peer", port), nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxMessageSize)
	p := &Peer{
		c:       c,
		pending: make(map[string]chan peerResponse),
		done:    make(chan struct{}),
	}
	go p.readLoop()
	return p, nil
}

// Done is closed when the owner connection dies, signalling a re-election.
func (p *Peer) Done() <-chan struct{} { return p.done }

func (p *Peer) readLoop() {
	ctx := context.Background()
	for {
		var resp peerResponse
		if err := wsjson.Read(ctx, p.c, &resp); err != nil {
			p.mu.Lock()
			for id, ch := range p.pending {
				ch <- peerResponse{ID: id, Error: "bridge owner disconnected, request abandoned"}
				delete(p.pending, id)
			}
			p.mu.Unlock()
			p.c.CloseNow()
			close(p.done)
			return
		}
		p.mu.Lock()
		ch, ok := p.pending[resp.ID]
		delete(p.pending, resp.ID)
		p.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// Call forwards a command to the owner and waits for the response. The owner
// enforces the timeout against the plugin; the local wait adds a grace
// period to cover transit.
func (p *Peer) Call(ctx context.Context, file, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	id := fmt.Sprintf("p%d", p.nextID.Add(1))
	ch := make(chan peerResponse, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	unregister := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}

	req := peerRequest{ID: id, File: file, Command: command, Params: raw, TimeoutMs: timeout.Milliseconds()}
	p.writeMu.Lock()
	err = wsjson.Write(ctx, p.c, req)
	p.writeMu.Unlock()
	if err != nil {
		unregister()
		return nil, fmt.Errorf("send to bridge owner: %w", err)
	}

	timer := time.NewTimer(timeout + 10*time.Second)
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
		return nil, fmt.Errorf("bridge owner did not answer %q within %s", command, timeout)
	}
}

// Files asks the owner for the list of connected Figma files.
func (p *Peer) Files(ctx context.Context) ([]FileInfo, error) {
	raw, err := p.Call(ctx, "", cmdListFiles, nil, DefaultTimeout)
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, fmt.Errorf("bad file list from bridge owner: %w", err)
	}
	return files, nil
}
