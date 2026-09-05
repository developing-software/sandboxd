package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

// One sandbox from POST to keystroke, with a worker on the other end of the tunnel. It is
// the phase 3 checklist in miniature: place, start, attach, type, resize, hang up.

const joinToken = "jt"

// tunnelWorker is a worker as far as the wire is concerned, and nothing more.
type tunnelWorker struct {
	t   *testing.T
	ctx context.Context
	ws  *websocket.Conn
}

func (a *api) connectWorker() *tunnelWorker {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	a.t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/tunnel"
	// coder/websocket documents that the dial response body needs no closing.
	ws, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { _ = ws.CloseNow() })

	w := &tunnelWorker{t: a.t, ctx: ctx, ws: ws}
	w.send(&wire.Hello{
		Name: "box", Fingerprint: "fp", Running: []string{},
		MaxSandboxes: 4, Tags: []string{"driver:docker"}, JoinToken: joinToken,
	})
	if _, is := w.control().(*wire.HelloOK); !is {
		a.t.Fatal("the join token should have approved this host on its first hello")
	}
	return w
}

func (w *tunnelWorker) send(m wire.FromHost) {
	w.t.Helper()
	b, err := wire.Marshal(m)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.ws.Write(w.ctx, websocket.MessageText, b); err != nil {
		w.t.Fatal(err)
	}
}

func (w *tunnelWorker) sendFrame(stream uint32, payload []byte) {
	w.t.Helper()
	if err := w.ws.Write(w.ctx, websocket.MessageBinary, wire.Encode(stream, payload)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *tunnelWorker) control() wire.FromCP {
	w.t.Helper()
	for {
		typ, data, err := w.ws.Read(w.ctx)
		if err != nil {
			w.t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		m, err := wire.ParseFromCP(data)
		if err != nil {
			w.t.Fatal(err)
		}
		return m
	}
}

func (w *tunnelWorker) frame() (uint32, []byte) {
	w.t.Helper()
	for {
		typ, data, err := w.ws.Read(w.ctx)
		if err != nil {
			w.t.Fatalf("read: %v", err)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		stream, payload, err := wire.Decode(data)
		if err != nil {
			w.t.Fatal(err)
		}
		return stream, payload
	}
}

// awaitStatus polls the API the way a client would.
func (a *api) awaitStatus(sid string, want store.SandboxStatus) cp.SandboxView {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last cp.SandboxView
	for time.Now().Before(deadline) {
		last = decode[cp.SandboxView](a.t, a.do(http.MethodGet, "/sandboxes/"+sid+"?owner_id=me", nil))
		if last.Status == want {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.t.Fatalf("sandbox stayed %s, want %s", last.Status, want)
	return last
}

func TestASandboxFromPostToKeystroke(t *testing.T) {
	a := newAPI(t)
	w := a.connectWorker()

	created := a.do(http.MethodPost, "/sandboxes", map[string]any{
		"owner_id": "me", "image": "img:1", "tags": []string{"driver:docker"},
		"secret_env": map[string]string{"GIT_TOKEN": "0-secret-0"},
	})
	if created.status != 201 {
		t.Fatalf("create = %d", created.status)
	}
	v := decode[cp.SandboxView](t, created)
	// A host with room takes it straight away, so the caller never sees `queued`.
	if v.Status != store.Creating {
		t.Fatalf("status = %s, want it placed on the connected host", v.Status)
	}

	order, is := w.control().(*wire.SandboxCreate)
	if !is || order.Spec.SID != v.ID {
		t.Fatalf("the worker was told %+v", order)
	}
	// The one place secrets travel, and they got here without passing through a column.
	if order.Spec.SecretEnv["GIT_TOKEN"] != "0-secret-0" {
		t.Errorf("secret_env = %v", order.Spec.SecretEnv)
	}
	if raw, _ := a.store.Dump("sandboxes"); strings.Contains(raw, "0-secret-0") {
		t.Errorf("a secret reached the database:\n%s", raw)
	}

	w.send(&wire.SandboxStarted{SID: v.ID})
	running := a.awaitStatus(v.ID, store.Running)
	if running.HostOnline == nil || !*running.HostOnline {
		t.Errorf("host_online = %v", running.HostOnline)
	}

	// A terminal, from the token the parent app mints for the browser.
	minted := a.do(http.MethodPost, "/sandboxes/"+v.ID+"/attach-token", map[string]any{"owner_id": "me"})
	if minted.status != 200 {
		t.Fatalf("attach-token = %d", minted.status)
	}
	token := decode[cp.AttachToken](t, minted).Token

	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/attach?token=" + token + "&cols=80&rows=24"
	// coder/websocket documents that the dial response body needs no closing.
	browser, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = browser.CloseNow() }()

	open, is := w.control().(*wire.PtyOpen)
	if !is || open.SID != v.ID || open.Size.Cols != 80 || open.Size.Rows != 24 {
		t.Fatalf("pty.open = %+v", open)
	}

	// Sandbox to browser.
	w.sendFrame(open.Stream, []byte("$ "))
	typ, data, err := browser.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "$ " {
		t.Errorf("browser read %v %q", typ, data)
	}

	// Browser to sandbox.
	if err := browser.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	stream, payload := w.frame()
	if stream != open.Stream || string(payload) != "ls\n" {
		t.Errorf("worker read %d %q", stream, payload)
	}

	// A resized window is a text frame, and reaches the sandbox as one message.
	resize, err := json.Marshal(map[string]any{"type": "resize", "cols": 100, "rows": 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := browser.Write(ctx, websocket.MessageText, resize); err != nil {
		t.Fatal(err)
	}
	got, is := w.control().(*wire.PtyResize)
	if !is || got.SID != v.ID || got.Size.Cols != 100 || got.Size.Rows != 30 {
		t.Errorf("pty.resize = %+v", got)
	}

	// The process in the terminal returns: the browser is told why, not just dropped.
	w.send(&wire.PtyClosed{Stream: open.Stream})
	typ, data, err = browser.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var closed struct{ Type, Reason string }
	if err := json.Unmarshal(data, &closed); err != nil || typ != websocket.MessageText {
		t.Fatalf("last frame = %v %q", typ, data)
	}
	if closed.Type != "closed" || closed.Reason != "pty closed" {
		t.Errorf("= %+v", closed)
	}

	// And the sandbox ending reaches the API.
	w.send(&wire.SandboxEnded{SID: v.ID, Reason: wire.EndExited, Detail: "shell exited"})
	ended := a.awaitStatus(v.ID, store.Ended)
	if ended.EndedReason == nil || *ended.EndedReason != wire.EndExited {
		t.Errorf("ended_reason = %v", ended.EndedReason)
	}
	if ended.EndedDetail == nil || *ended.EndedDetail != "shell exited" {
		t.Errorf("ended_detail = %v", ended.EndedDetail)
	}
}

func TestABrowserLeavingClosesItsPTY(t *testing.T) {
	a := newAPI(t)
	w := a.connectWorker()

	v := decode[cp.SandboxView](t, a.do(http.MethodPost, "/sandboxes",
		map[string]any{"owner_id": "me", "image": "i"}))
	if _, is := w.control().(*wire.SandboxCreate); !is {
		t.Fatal("expected sandbox.create")
	}
	w.send(&wire.SandboxStarted{SID: v.ID})
	a.awaitStatus(v.ID, store.Running)

	token := decode[cp.AttachToken](t, a.do(http.MethodPost,
		"/sandboxes/"+v.ID+"/attach-token", map[string]any{"owner_id": "me"})).Token
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/attach?token=" + token
	// coder/websocket documents that the dial response body needs no closing.
	browser, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	open, is := w.control().(*wire.PtyOpen)
	if !is {
		t.Fatal("expected pty.open")
	}

	// The tab is closed while the sandbox is still running and its terminal still quiet.
	if err := browser.CloseNow(); err != nil {
		t.Fatal(err)
	}
	got, is := w.control().(*wire.PtyClose)
	if !is || got.Stream != open.Stream {
		t.Fatalf("the host was told %+v, want pty.close for stream %d", got, open.Stream)
	}

	// And the handler let go of the request. A shutdown is the cheapest way to see it:
	// with both sockets gone there is nothing left in flight, so Close returns at once.
	if err := w.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	shut := make(chan struct{})
	go func() { a.srv.Close(); close(shut) }()
	select {
	case <-shut:
	case <-time.After(5 * time.Second):
		t.Fatal("a handler is still running with nobody on the other end of it")
	}
}

func TestADeleteReachesTheWorker(t *testing.T) {
	a := newAPI(t)
	w := a.connectWorker()

	v := decode[cp.SandboxView](t, a.do(http.MethodPost, "/sandboxes",
		map[string]any{"owner_id": "me", "image": "i"}))
	if _, is := w.control().(*wire.SandboxCreate); !is {
		t.Fatal("expected sandbox.create")
	}
	w.send(&wire.SandboxStarted{SID: v.ID})
	a.awaitStatus(v.ID, store.Running)

	ended := decode[cp.SandboxView](t,
		a.do(http.MethodDelete, "/sandboxes/"+v.ID+"?owner_id=me", nil))
	// The host is online, so the row waits for it to confirm rather than lying.
	if ended.Status != store.Running {
		t.Errorf("status right after DELETE = %s", ended.Status)
	}
	msg := w.control()
	got, is := msg.(*wire.SandboxDestroy)
	if !is || got.SID != v.ID {
		t.Fatalf("the worker was told %T %+v", msg, msg)
	}
	w.send(&wire.SandboxEnded{SID: v.ID, Reason: wire.EndClosed})
	a.awaitStatus(v.ID, store.Ended)
}
