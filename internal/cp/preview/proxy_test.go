package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
)

// The proxy against real HTTP: a real server stands in for the app inside the sandbox, and
// the dialler is the only fake, because the tunnel is the boundary we do not own here.

const domain = "preview.example.com"

// fakeDialer maps a sandbox id to something to connect to. The real one hands back a
// tunnel stream; both are net.Conn, which is the point of the design.
type fakeDialer struct {
	mu      sync.Mutex
	addrs   map[string]string // sid -> host:port of the app standing in for the sandbox
	offline map[string]bool
	dials   []string
	fail    error
}

func (d *fakeDialer) Online(hostID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.offline[hostID]
}

func (d *fakeDialer) Dial(ctx context.Context, _, sid string, port int) (net.Conn, error) {
	d.mu.Lock()
	if d.fail != nil {
		d.mu.Unlock()
		return nil, d.fail
	}
	addr, ok := d.addrs[sid]
	d.dials = append(d.dials, fmt.Sprintf("%s:%d", sid, port))
	d.mu.Unlock()
	if !ok {
		return nil, errors.New("no such sandbox")
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", addr)
}

func (d *fakeDialer) dialled() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dials...)
}

type harness struct {
	t      *testing.T
	store  *store.SQLite
	dialer *fakeDialer
	tokens *cp.Tokens
	proxy  *Proxy
	srv    *httptest.Server
	client *http.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	st, err := store.Open(":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dialer := &fakeDialer{addrs: map[string]string{}, offline: map[string]bool{}}
	tokens := cp.NewTokens("k")
	// The store satisfies the proxy's lookup directly: there is nothing to fake.
	proxy := NewProxy(st, dialer, tokens, domain, log)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := proxy.Match(r)
		if !ok {
			http.Error(w, "not a preview host", http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, r, target)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{
		// The redirect after the token exchange is the thing under test, not a detour.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &harness{t: t, store: st, dialer: dialer, tokens: tokens, proxy: proxy, srv: srv, client: client}
}

// sandbox registers a running sandbox whose port answers with the given handler.
func (h *harness) sandbox(sid string, handler http.Handler) {
	h.t.Helper()
	err := h.store.InsertPendingHost(store.Host{ID: "h1", Name: "h1", Fingerprint: "fp" + sid})
	if err != nil && !strings.Contains(err.Error(), "UNIQUE") {
		h.t.Fatal(err)
	}
	if err := h.store.InsertSandbox(store.Sandbox{ID: sid, OwnerID: "me", Image: "i", CreatedAt: 1}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.MarkCreating(sid, "h1"); err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.MarkRunning(sid); err != nil {
		h.t.Fatal(err)
	}
	app := httptest.NewServer(handler)
	h.t.Cleanup(app.Close)
	h.dialer.mu.Lock()
	h.dialer.addrs[sid] = app.Listener.Addr().String()
	h.dialer.mu.Unlock()
}

func (h *harness) host(sid string, port int) string {
	return fmt.Sprintf("%d-%s.%s", port, sid, domain)
}

// reply is a finished exchange: the body is read and the connection released, so a test
// never holds one open.
type reply struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    string
}

// get sends a request to the proxy as though it had arrived on a preview host.
func (h *harness) get(sid string, port int, path string, cookies ...*http.Cookie) reply {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	// The Host header is the routing, and it is not where we dialled.
	req.Host = h.host(sid, port)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return h.send(req)
}

// send runs an already-built request and reads it to the end.
func (h *harness) send(req *http.Request) reply {
	h.t.Helper()
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return reply{
		status: res.StatusCode, header: res.Header, cookies: res.Cookies(), body: string(raw),
	}
}

// cookie runs the token exchange and returns what the browser would keep.
func (h *harness) cookie(sid string, port int) *http.Cookie {
	h.t.Helper()
	token := h.tokens.Sign(
		cp.TokenPayload{Kind: cp.TokenPreview, SID: sid, Port: port}, time.Minute)
	res := h.get(sid, port, "/app?keep=1&t="+token)
	if res.status != http.StatusFound {
		h.t.Fatalf("exchange = %d, want a redirect", res.status)
	}
	// The clean URL keeps the caller's own query and drops only ours.
	if loc := res.header.Get("Location"); loc != "/app?keep=1" {
		h.t.Errorf("redirected to %q", loc)
	}
	for _, c := range res.cookies {
		if c.Name == Cookie {
			return c
		}
	}
	h.t.Fatal("no preview cookie was set")
	return nil
}

func echo(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		w.Header().Set("X-App", "yes")
		fmt.Fprintf(w, "host=%s\n", r.Host)
		fmt.Fprintf(w, "path=%s\n", r.URL.RequestURI())
		fmt.Fprintf(w, "xfh=%s\n", r.Header.Get("X-Forwarded-Host"))
		fmt.Fprintf(w, "xfp=%s\n", r.Header.Get("X-Forwarded-Proto"))
		fmt.Fprintf(w, "cookie=%s\n", r.Header.Get("Cookie"))
	})
}

func TestMatchOnlyPreviewHosts(t *testing.T) {
	h := newHarness(t)
	cases := map[string]bool{
		"3000-s_ab34.preview.example.com":      true,
		"3000-s_ab34.preview.example.com:8080": true,
		"3000-S_AB34.PREVIEW.EXAMPLE.COM":      true,
		"cp.example.com":                       false,
		"s_ab34.preview.example.com":           false, // no port
		"3000-nope.preview.example.com":        false, // not a sandbox id
		"3000-s_ab34.evil.com":                 false,
		"70000-s_ab34.preview.example.com":     false, // not a port
	}
	for host, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		got, ok := h.proxy.Match(req)
		if ok != want {
			t.Errorf("Match(%q) = %v, want %v", host, ok, want)
		}
		if ok && got.SID != "s_ab34" {
			t.Errorf("Match(%q) sid = %q, want it lowercased", host, got.SID)
		}
	}
}

func TestTheFirstRequestTradesItsTokenForACookie(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))

	c := h.cookie("s_a", 3000)
	if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", c)
	}
	if c.Secure {
		t.Error("a cookie marked Secure never comes back over the plain http of local dev")
	}
	if _, ok := h.tokens.Verify(c.Value, cp.TokenPreviewCookie); !ok {
		t.Error("the cookie is not one of ours")
	}
	// Nothing was proxied on the way: the exchange answers by itself.
	if got := h.dialer.dialled(); len(got) != 0 {
		t.Errorf("dialled %v during the exchange", got)
	}
}

func TestWithoutAGoodCredentialNothingIsProxied(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	good := h.cookie("s_a", 3000)
	wrongSandbox := h.tokens.Sign(
		cp.TokenPayload{Kind: cp.TokenPreview, SID: "s_b", Port: 3000}, time.Minute)

	cases := map[string]struct {
		port    int
		cookies []*http.Cookie
		path    string
	}{
		"no cookie at all":               {3000, nil, "/"},
		"a cookie we did not sign":       {3000, []*http.Cookie{{Name: Cookie, Value: "forged"}}, "/"},
		"the right cookie, another port": {3001, []*http.Cookie{good}, "/"},
		"a token for another sandbox":    {3000, nil, "/?t=" + wrongSandbox},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res := h.get("s_a", c.port, c.path, c.cookies...)
			if res.status != http.StatusUnauthorized {
				t.Errorf("= %d, want 401", res.status)
			}
		})
	}
	if got := h.dialer.dialled(); len(got) != 0 {
		t.Errorf("dialled %v without a credential", got)
	}
}

func TestASandboxThatIsNotThereIsA502(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	c := h.cookie("s_a", 3000)

	// Ended, not missing: the URL is valid, the sandbox is not there.
	if err := h.store.MarkEnded("s_a", "closed", ""); err != nil {
		t.Fatal(err)
	}
	res := h.get("s_a", 3000, "/", c)
	if res.status != http.StatusBadGateway {
		t.Fatalf("= %d, want 502 rather than 404", res.status)
	}
	if !strings.Contains(res.body, "not running") {
		t.Error("the answer says which of the two it is")
	}
}

func TestAnOfflineHostIsA502(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	c := h.cookie("s_a", 3000)
	h.dialer.mu.Lock()
	h.dialer.offline["h1"] = true
	h.dialer.mu.Unlock()

	res := h.get("s_a", 3000, "/", c)
	if res.status != http.StatusBadGateway || !strings.Contains(res.body, "offline") {
		t.Errorf("= %d %q", res.status, res.body)
	}
}

func TestAPortNothingIsListeningOnIsA502(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	c := h.cookie("s_a", 3000)
	h.dialer.mu.Lock()
	h.dialer.fail = errors.New("connection refused")
	h.dialer.mu.Unlock()

	res := h.get("s_a", 3000, "/", c)
	if res.status != http.StatusBadGateway {
		t.Fatalf("= %d", res.status)
	}
	if got := res.body; !strings.Contains(got, "could not reach port 3000") {
		t.Errorf("body = %q", got)
	}
}

func TestWhatTheAppInsideTheSandboxSees(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	c := h.cookie("s_a", 3000)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.srv.URL+"/deep/path?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = h.host("s_a", 3000)
	req.AddCookie(c)
	req.AddCookie(&http.Cookie{Name: "app_session", Value: "theirs"})
	res := h.send(req)

	if res.status != http.StatusOK || res.header.Get("X-App") != "yes" {
		t.Fatalf("= %d, headers %v", res.status, res.header)
	}
	got := res.body
	// The app is at localhost, and its own links should say so.
	if !strings.Contains(got, "host=localhost:3000") {
		t.Errorf("host = %q", got)
	}
	if !strings.Contains(got, "path=/deep/path?q=1") {
		t.Errorf("path = %q", got)
	}
	// The public name it was reached by, for an app that builds absolute URLs.
	if !strings.Contains(got, "xfh="+h.host("s_a", 3000)) {
		t.Errorf("x-forwarded-host = %q", got)
	}
	if !strings.Contains(got, "xfp=http") {
		t.Errorf("x-forwarded-proto = %q", got)
	}
	// Our cookie is a capability for this proxy; the app has no business reading it.
	if strings.Contains(got, Cookie) {
		t.Errorf("the preview cookie reached the sandbox: %q", got)
	}
	if !strings.Contains(got, "app_session=theirs") {
		t.Errorf("the app's own cookie was dropped: %q", got)
	}
}

func TestAFrontProxysForwardedHeadersWin(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", echo(t))
	c := h.cookie("s_a", 3000)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = h.host("s_a", 3000)
	req.AddCookie(c)
	// Only the front proxy knows the browser used TLS; we cannot see it from here.
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "3000-s_a.preview.production.example")
	res := h.send(req)

	got := res.body
	if !strings.Contains(got, "xfp=https") || !strings.Contains(got, "xfh=3000-s_a.preview.production.example") {
		t.Errorf("the front proxy's headers were overwritten: %q", got)
	}
}

func TestEachSandboxGetsItsOwnUpstream(t *testing.T) {
	h := newHarness(t)
	// Two sandboxes on the same port number, behind one shared transport: http.Transport
	// pools by scheme and host, so the outbound host has to carry the sandbox id or a
	// request for one sandbox is answered by another.
	h.sandbox("s_a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "I am A")
	}))
	h.sandbox("s_b", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "I am B")
	}))
	ca, cb := h.cookie("s_a", 3000), h.cookie("s_b", 3000)

	for range 3 {
		if got := h.get("s_a", 3000, "/", ca).body; got != "I am A" {
			t.Fatalf("s_a answered %q", got)
		}
		if got := h.get("s_b", 3000, "/", cb).body; got != "I am B" {
			t.Fatalf("s_b answered %q", got)
		}
	}
}

func TestWebSocketsPassThrough(t *testing.T) {
	h := newHarness(t)
	h.sandbox("s_a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
			Subprotocols:   []string{"jupyter"},
		})
		if err != nil {
			t.Errorf("the app could not accept: %v", err)
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			typ, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if err := ws.Write(r.Context(), typ, append([]byte("echo:"), data...)); err != nil {
				return
			}
		}
	}))
	c := h.cookie("s_a", 3000)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Dial the preview host by name, over a transport that goes to the proxy: the Host
	// header is the routing, and it has to be the real one.
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(h.srv.URL, "http://"))
		},
	}}
	// coder/websocket documents that the dial response body needs no closing.
	ws, res, err := websocket.Dial(ctx, "ws://"+h.host("s_a", 3000)+"/ws", &websocket.DialOptions{ //nolint:bodyclose
		HTTPClient:   client,
		HTTPHeader:   http.Header{"Cookie": {c.String()}},
		Subprotocols: []string{"jupyter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	// The upgrade's own headers survive the round trip, which is what a notebook needs.
	if got := res.Header.Get("Sec-WebSocket-Protocol"); got != "jupyter" {
		t.Errorf("negotiated subprotocol = %q", got)
	}

	if err := ws.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "echo:hello" {
		t.Errorf("read %q", data)
	}
}
