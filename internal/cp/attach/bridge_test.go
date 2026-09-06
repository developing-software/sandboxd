package attach

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// The bridge against a real WebSocket and the real store. The PTY is the one fake: it is
// the tunnel's end of the world, and a pipe is what it looks like from here.

const (
	running = "s_running"
	hostID  = "h_1"
)

// fakePTY is a terminal made of a pipe: the test writes what the process "prints" into
// out, and reads what the browser typed from typed.
type fakePTY struct {
	outR  *io.PipeReader
	outW  *io.PipeWriter
	typed chan []byte
	sizes chan wire.Size
	once  sync.Once
}

func newFakePTY() *fakePTY {
	r, w := io.Pipe()
	return &fakePTY{outR: r, outW: w, typed: make(chan []byte, 16), sizes: make(chan wire.Size, 16)}
}

func (p *fakePTY) Read(b []byte) (int, error) { return p.outR.Read(b) }

func (p *fakePTY) Write(b []byte) (int, error) {
	p.typed <- slices.Clone(b)
	return len(b), nil
}

func (p *fakePTY) Close() error {
	p.once.Do(func() { _ = p.outR.Close(); _ = p.outW.Close() })
	return nil
}

func (p *fakePTY) Resize(size wire.Size) error {
	p.sizes <- size
	return nil
}

// fakeHub answers OpenPTY with one PTY, or one error.
type fakeHub struct {
	pty *fakePTY
	err error
}

func (h *fakeHub) OpenPTY(_, _ string, _ wire.Size) (PTY, error) {
	if h.err != nil {
		return nil, h.err
	}
	return h.pty, nil
}

type harness struct {
	t   *testing.T
	ctx context.Context
	srv *httptest.Server
	hub *fakeHub
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	st, err := store.Open(":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// One running sandbox on one host, the way the scheduler would have written them.
	if err := st.InsertPendingHost(store.Host{ID: hostID, Name: "box", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	sb := store.Sandbox{
		ID: running, OwnerID: "me", Status: store.Queued, Image: "img",
		Env: map[string]string{}, IdleTimeoutS: 60, CreatedAt: time.Now().UnixMilli(),
	}
	if err := st.InsertSandbox(sb); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCreating(running, hostID); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRunning(running); err != nil {
		t.Fatal(err)
	}

	hub := &fakeHub{pty: newFakePTY()}
	bridge := NewBridge(st, hub, log)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridge.Serve(w, r, r.URL.Query().Get("sid"), wire.Size{Cols: 80, Rows: 24})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return &harness{t: t, ctx: ctx, srv: srv, hub: hub}
}

// attach opens a browser on the given sandbox.
func (h *harness) attach(sid string) *websocket.Conn {
	h.t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/?sid=" + sid
	// coder/websocket documents that the dial response body needs no closing.
	ws, _, err := websocket.Dial(h.ctx, url, nil) //nolint:bodyclose
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// closedWith reads the next frame and requires it to be the farewell.
func (h *harness) closedWith(ws *websocket.Conn, want string) {
	h.t.Helper()
	typ, data, err := ws.Read(h.ctx)
	if err != nil {
		h.t.Fatalf("read: %v (wanted a closed frame saying %q)", err, want)
	}
	var msg serverMsg
	if typ != websocket.MessageText || json.Unmarshal(data, &msg) != nil {
		h.t.Fatalf("frame = %v %q, want a closed frame", typ, data)
	}
	if msg.Type != "closed" || !strings.Contains(msg.Reason, want) {
		h.t.Errorf("closed = %+v, want reason containing %q", msg, want)
	}
}

// The farewell is the reason the bridge exists as a protocol: a browser must learn why,
// not just see the socket drop. It is written on the same goroutine that is being told to
// stop, so it is run many times — the race it guards against surfaced in about one run in
// two before the fix.
func TestAFailedOpenSaysWhyBeforeClosing(t *testing.T) {
	h := newHarness(t)
	for range 25 {
		h.closedWith(h.attach("s_missing"), "sandbox not running")
	}
}

func TestAnOfflineHostSaysSo(t *testing.T) {
	h := newHarness(t)
	h.hub.err = errors.New("hosts: host is offline")
	h.closedWith(h.attach(running), "host offline")
}

func TestBytesFlowBothWaysAndResizeReachesThePTY(t *testing.T) {
	h := newHarness(t)
	pty := h.hub.pty
	ws := h.attach(running)

	// Keystrokes: binary from the browser, verbatim into the PTY.
	if err := ws.Write(h.ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := <-pty.typed; string(got) != "ls\r" {
		t.Errorf("pty received %q", got)
	}

	// Output: whatever the process prints, binary to the browser.
	go func() { _, _ = pty.outW.Write([]byte("total 0\r\n")) }()
	typ, data, err := ws.Read(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "total 0\r\n" {
		t.Errorf("browser received %v %q", typ, data)
	}

	// A resize is a text frame, and lands on the PTY as a Size.
	if err := ws.Write(h.ctx, websocket.MessageText, []byte(`{"type":"resize","cols":132,"rows":50}`)); err != nil {
		t.Fatal(err)
	}
	if got := <-pty.sizes; got != (wire.Size{Cols: 132, Rows: 50}) {
		t.Errorf("resize = %+v", got)
	}

	// Nonsense in a text frame is ignored, not fatal.
	if err := ws.Write(h.ctx, websocket.MessageText, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}

	// The process exits: the browser is told, then the socket closes.
	_ = pty.Close()
	h.closedWith(ws, "pty closed")
	if _, _, err := ws.Read(h.ctx); err == nil {
		t.Error("the socket should be closed after the farewell")
	}
}
