package hosts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/wire"
)

// The connection's own policies, as opposed to the hub's routing: what a repeated answer
// from the worker does, and what a queue nobody is draining does. Both are the worker
// misbehaving, which is a peer we do not control.

// TestASecondPortOpenIsIgnored: the host names the stream, so it can answer one twice.
// Before the fix the second answer closed the same channel again and panicked the read
// goroutine, taking the tunnel down with it.
func TestASecondPortOpenIsIgnored(t *testing.T) {
	h := newHubHarness(t, "jt")
	w, hostID := h.accepted("fp1")

	errs := make(chan error, 1)
	go func() {
		_, err := h.hub.Dial(h.ctx, hostID, "s_1", 3000)
		errs <- err
	}()
	if _, is := w.control().(*wire.PortDial); !is {
		t.Fatal("expected port.dial")
	}
	w.send(&wire.PortOpen{Stream: 1})
	if err := <-errs; err != nil {
		t.Fatal(err)
	}

	// The answer the worker had no business sending. The first one stands.
	w.send(&wire.PortOpen{Stream: 1})
	w.send(&wire.PortError{Stream: 1, Msg: "too late"})

	// The socket is still there and still routing, which is the whole assertion: a panic
	// in the read goroutine would have ended it.
	if _, err := h.hub.OpenPTY(hostID, "s_2", wire.Size{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("the tunnel did not survive a repeated answer: %v", err)
	}
	if got, is := w.control().(*wire.PtyOpen); !is || got.SID != "s_2" {
		t.Errorf("pty.open = %+v", got)
	}
}

// TestAStalledControlQueueDropsTheTunnelNotTheCaller: the caller is usually the scheduler
// goroutine, the only writer of sandbox state, so a wedged worker must cost it one
// controlWait and not the whole writeTimeout.
func TestAStalledControlQueueDropsTheTunnelNotTheCaller(t *testing.T) {
	c := newConn(rawSocket(t), discard())
	// No writeLoop is started: an undrained queue is what a wedged socket looks like from
	// the sending side.
	for range writeQueue {
		c.out <- outgoing{typ: websocket.MessageText, data: []byte("{}")}
	}

	start := time.Now()
	err := c.send(&wire.SandboxDestroy{SID: "s_1"})
	took := time.Since(start)

	if !errors.Is(err, errHostGone) {
		t.Fatalf("send = %v, want errHostGone so the sandbox stays queued", err)
	}
	if took > 3*controlWait {
		t.Errorf("send blocked for %s; the scheduler may not wait that long", took)
	}
	select {
	case <-c.done:
	default:
		t.Error("a host that cannot take a control frame must be dropped, not left connected")
	}
}

// rawSocket is a live WebSocket with nothing reading the other end, for driving a Conn's
// own queue without the hub's read loop in the way.
func rawSocket(t *testing.T) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), wait)
	t.Cleanup(cancel)
	// coder/websocket documents that the dial response body needs no closing by the
	// caller, so bodyclose's report here is a false positive.
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}
