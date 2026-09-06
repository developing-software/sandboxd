// Package attach bridges a browser terminal to a sandbox's PTY. Binary frames are raw
// PTY bytes in both directions; text frames are JSON — the browser sends a resize, the
// control plane sends the reason it is going away.
package attach

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// viewerQueue is how much PTY output may be waiting for one browser. Full means the
// viewer is dropped, not the tunnel stalled: the worker's ring replays the last 256 KB
// when the browser reattaches, which is what makes dropping safe here.
const viewerQueue = 256

// PTY is what the bridge needs of a terminal: bytes both ways, a close, and a resize. The
// hub's tunnel stream is one; the interface is declared here so this package depends on
// the wire and the store and never on the tunnel, and so the bridge can be tested against
// a pipe.
type PTY interface {
	io.ReadWriteCloser
	Resize(size wire.Size) error
}

// What the bridge needs from the rest of the control plane.
type (
	sandboxes interface {
		Sandbox(sid string) (store.Sandbox, bool, error)
	}
	// Opener attaches a viewer to a running sandbox's terminal on its host.
	Opener interface {
		OpenPTY(hostID, sid string, size wire.Size) (PTY, error)
	}
)

// Open adapts a function that returns any concrete PTY — the hub's `OpenPTY` returns its
// own stream type — so the producer keeps exporting a struct and this package never names
// it. The nil check is the point of doing it here once: a nil pointer returned through an
// interface is not a nil interface.
func Open[P PTY](open func(hostID, sid string, size wire.Size) (P, error)) Opener {
	return openFunc(func(hostID, sid string, size wire.Size) (PTY, error) {
		p, err := open(hostID, sid, size)
		if err != nil {
			return nil, err
		}
		return p, nil
	})
}

type openFunc func(hostID, sid string, size wire.Size) (PTY, error)

func (f openFunc) OpenPTY(hostID, sid string, size wire.Size) (PTY, error) {
	return f(hostID, sid, size)
}

type Bridge struct {
	store sandboxes
	hub   Opener
	log   *slog.Logger
}

func NewBridge(s sandboxes, hub Opener, log *slog.Logger) *Bridge {
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
	v := &viewer{
		ws:     ws,
		out:    make(chan []byte, viewerQueue),
		reason: make(chan string, 1),
		stop:   make(chan struct{}),
	}
	done := make(chan struct{})
	go func() { defer close(done); v.write(ctx) }()
	// Told to go, then waited for: the request context is still live while this handler
	// returns, so nothing else would wake the writer.
	defer func() { v.shutdown(); <-done }()

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
func (b *Bridge) open(sid string, size wire.Size) (PTY, error) {
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
	stop   chan struct{}
	once   sync.Once
}

func (v *viewer) write(ctx context.Context) {
	defer func() { _ = v.ws.CloseNow() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-v.stop:
			// A reason queued just before the stop must still go out: `close` then
			// `shutdown` is the normal order when opening fails, and with both channels
			// ready a select picks at random — this is the test that flaked without it.
			select {
			case reason := <-v.reason:
				v.farewell(ctx, reason)
			default:
			}
			return
		case reason := <-v.reason:
			v.farewell(ctx, reason)
			return
		case b := <-v.out:
			if err := v.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}

// farewell is the last frame: why the terminal is going away.
func (v *viewer) farewell(ctx context.Context, reason string) {
	msg, err := json.Marshal(serverMsg{Type: "closed", Reason: reason})
	if err == nil {
		_ = v.ws.Write(ctx, websocket.MessageText, msg)
	}
}

// send hands over a copy: the read buffer above is reused on the next iteration.
func (v *viewer) send(b []byte) {
	select {
	case v.out <- append([]byte(nil), b...):
	default: // dropped; the ring replays on reattach
	}
}

// close asks the writer to say why it is going, and then to go.
func (v *viewer) close(reason string) {
	select {
	case v.reason <- reason:
	default: // already closing, and the first reason is the true one
	}
}

// shutdown ends the writer without a word, for when there is nobody left to tell.
func (v *viewer) shutdown() {
	v.once.Do(func() { close(v.stop) })
}
