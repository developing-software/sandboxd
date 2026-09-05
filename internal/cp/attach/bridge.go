// Package attach bridges a browser terminal to a sandbox's PTY. Binary frames are raw
// PTY bytes in both directions; text frames are JSON — the browser sends a resize, the
// control plane sends the reason it is going away.
package attach

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"sandboxd/internal/cp/hosts"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// viewerQueue is how much PTY output may be waiting for one browser. Full means the
// viewer is dropped, not the tunnel stalled: the worker's ring replays the last 256 KB
// when the browser reattaches, which is what makes dropping safe here.
const viewerQueue = 256

// What the bridge needs. The opener returns the concrete stream because a PTY is a
// stream with a Resize, and there is nothing else it could be.
type (
	sandboxes interface {
		Sandbox(sid string) (store.Sandbox, bool, error)
	}
	opener interface {
		OpenPTY(hostID, sid string, size wire.Size) (*hosts.Stream, error)
	}
)

type Bridge struct {
	store sandboxes
	hub   opener
	log   *slog.Logger
}

func NewBridge(s sandboxes, hub opener, log *slog.Logger) *Bridge {
	return &Bridge{store: s, hub: hub, log: log}
}

// clientMsg is everything the browser may say in a text frame.
type clientMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type serverMsg struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// Serve runs one attached terminal. The token has already been checked, and sid comes
// from it rather than from the request.
func (b *Bridge) Serve(w http.ResponseWriter, r *http.Request, sid string, size wire.Size) {
	// Any origin: the attach token is the gate, and the parent app is a different origin
	// by design (DESIGN.md decision 17 keeps the token holder off this server).
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		b.log.Warn("attach upgrade failed", "sid", sid, "err", err)
		return
	}
	ctx := r.Context()
	v := &viewer{ws: ws, out: make(chan []byte, viewerQueue), reason: make(chan string, 1)}
	done := make(chan struct{})
	go func() { defer close(done); v.write(ctx) }()
	defer func() { <-done }()

	pty, err := b.open(sid, size)
	if err != nil {
		v.close(err.Error())
		return
	}
	defer func() { _ = pty.Close() }()

	// PTY to browser, until the sandbox or the socket ends it.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				v.send(buf[:n])
			}
			if err != nil {
				v.close("pty closed")
				return
			}
		}
	}()

	// Browser to PTY, on this goroutine: when it ends, so does the attachment.
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := pty.Write(data); err != nil {
				v.close("pty closed")
				return
			}
			continue
		}
		var m clientMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue // a browser sending nonsense is not worth ending a terminal over
		}
		if m.Type == "resize" && m.Cols > 0 && m.Rows > 0 {
			if err := pty.Resize(wire.Size{Cols: m.Cols, Rows: m.Rows}); err != nil {
				b.log.Warn("attach resize", "sid", sid, "err", err)
			}
		}
	}
}

// open finds the sandbox and attaches to its host, or says why it could not.
func (b *Bridge) open(sid string, size wire.Size) (*hosts.Stream, error) {
	sb, found, err := b.store.Sandbox(sid)
	if err != nil {
		return nil, errors.New("sandbox lookup failed")
	}
	if !found || sb.Status != store.Running || sb.HostID == nil {
		return nil, errors.New("sandbox not running")
	}
	pty, err := b.hub.OpenPTY(*sb.HostID, sid, size)
	if err != nil {
		return nil, errors.New("host offline")
	}
	return pty, nil
}

// viewer is the browser side: one writer goroutine, fed by a channel, and nothing else
// touches the socket.
type viewer struct {
	ws     *websocket.Conn
	out    chan []byte
	reason chan string
}

func (v *viewer) write(ctx context.Context) {
	defer func() { _ = v.ws.CloseNow() }()
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-v.reason:
			msg, err := json.Marshal(serverMsg{Type: "closed", Reason: reason})
			if err == nil {
				_ = v.ws.Write(ctx, websocket.MessageText, msg)
			}
			return
		case b := <-v.out:
			if err := v.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}

// send hands over a copy: the read buffer above is reused on the next iteration.
func (v *viewer) send(b []byte) {
	select {
	case v.out <- append([]byte(nil), b...):
	default: // dropped; the ring replays on reattach
	}
}

func (v *viewer) close(reason string) {
	select {
	case v.reason <- reason:
	default: // already closing, and the first reason is the true one
	}
}
