package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// Router gives an MCP session one call surface regardless of its role in the
// bridge election. The first session to bind the bridge port becomes the
// owner and serves plugins and peers; later sessions join it as peers. When
// the owner exits, its peers re-run the election, so the bridge survives any
// one session closing — plugins reconnect to the new owner on their own.
type Router struct {
	mu      sync.Mutex
	owner   *Bridge
	peer    *Peer
	project string
	port    int
}

func NewRouter(project string, port int) *Router {
	return &Router{project: project, port: port}
}

// Run blocks forever, holding whichever role the election gives this session
// and re-running the election whenever that role ends.
func (r *Router) Run() {
	dialFailures := 0
	for {
		b := New()
		b.SetProject(r.project)
		if lns, err := b.listen(r.port); err == nil {
			dialFailures = 0
			log.Printf("bridge: this session owns the bridge on ws://localhost:%d", r.port)
			r.set(b, nil)
			serveErr := b.serve(lns)
			r.set(nil, nil)
			log.Printf("bridge: owner stopped serving: %v", serveErr)
		} else if p, perr := DialPeer(r.port); perr == nil {
			dialFailures = 0
			log.Printf("bridge: joined the bridge owner on port %d as a peer", r.port)
			r.set(nil, p)
			<-p.Done()
			r.set(nil, nil)
			log.Print("bridge: bridge owner went away, re-running election")
		} else {
			// Port taken but no peer endpoint: an old figma-mcp version or a
			// foreign process holds it. Keep trying, but don't spam the log.
			dialFailures++
			if dialFailures == 1 || dialFailures%60 == 0 {
				log.Printf("bridge: port %d is held by a process without the peer protocol "+
					"(update older figma-mcp sessions, or free the port): %v", r.port, perr)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (r *Router) set(b *Bridge, p *Peer) {
	r.mu.Lock()
	r.owner, r.peer = b, p
	r.mu.Unlock()
}

func (r *Router) roles() (*Bridge, *Peer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner, r.peer
}

var errElecting = errors.New(
	"the local Figma bridge is (re)connecting; retry in a few seconds")

// Call routes a command to the plugin serving the given file, either
// directly (owner) or via the owner (peer).
func (r *Router) Call(ctx context.Context, file, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	b, p := r.roles()
	switch {
	case b != nil:
		return b.CallFile(ctx, file, command, params, timeout)
	case p != nil:
		return p.Call(ctx, file, command, params, timeout)
	default:
		return nil, errElecting
	}
}

// Files lists the Figma files currently connected to the bridge.
func (r *Router) Files(ctx context.Context) ([]FileInfo, error) {
	b, p := r.roles()
	switch {
	case b != nil:
		return b.Files(), nil
	case p != nil:
		return p.Files(ctx)
	default:
		return nil, errElecting
	}
}
