package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sandboxd/api"
	"sandboxd/internal/adminapi"
	"sandboxd/internal/cp"
	"sandboxd/internal/cp/attach"
	"sandboxd/internal/cp/hosts"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/openapi"
)

// The whole control plane behind an httptest.Server, with no worker connected: every
// dependency is the real one, which is what makes the status codes worth asserting.

const (
	serviceToken = "secret"
	// owner is the value the X-Sandboxd-Owner header carries unless a test says otherwise.
	owner = "me"
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	srv    *httptest.Server
	store  *store.SQLite
	hub    *hosts.Hub
	tokens *cp.Tokens
	svc    *cp.Sandboxes
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	st, err := store.Open(":memory:", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := cp.Config{
		ServiceToken:  serviceToken,
		PublicURL:     "https://cp.example.com",
		PreviewDomain: "preview.example.com",
	}
	tokens := cp.NewTokens("k")
	events := make(chan cp.Event, 16)
	hub := hosts.NewHub(st, joinToken, events, log)
	sched := cp.NewScheduler(st, hub, nil, events, log)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go sched.Run(ctx)

	svc := cp.NewSandboxes(cfg, st, hub, sched, tokens)
	handler, err := New(Deps{
		ServiceToken: cfg.ServiceToken,
		Tokens:       tokens,
		Sandboxes:    svc,
		Hosts:        hosts.NewService(st, hub, log),
		Attach:       attach.NewBridge(svc, hub, log),
		Preview:      preview.NewProxy(svc, hub, tokens, cfg.PreviewDomain, log),
		Tunnel:       hub.Serve,
		Log:          log,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &harness{t: t, ctx: ctx, srv: srv, store: st, hub: hub, tokens: tokens, svc: svc}
}

// response is a finished exchange: the body is already read and the socket returned to
// the pool, so a test never has one open.
type response struct {
	path   string
	status int
	body   []byte
}

// do sends a request with the service token and the owner header unless told otherwise.
// An empty value in `headers` removes a header rather than blanking it, which is how a
// test asks what happens when a caller sends none.
func (h *harness) do(method, path string, body any, headers ...string) response {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(h.ctx, method, h.srv.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	req.Header.Set("X-Sandboxd-Owner", owner)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response{path: path, status: res.StatusCode, body: raw}
}

func decode[T any](t *testing.T, res response) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("decoding %s: %v in %s", res.path, err, res.body)
	}
	return out
}

type failure struct {
	Error  string `json:"error"`
	Issues []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	} `json:"issues"`
}

func TestAuth(t *testing.T) {
	h := newHarness(t)

	// What the two documents mark `security: []`, plus the pages that describe them.
	for _, path := range []string{"/healthz", "/openapi.yaml", "/openapi.admin.yaml", "/doc"} {
		res := h.do(http.MethodGet, path, nil, "Authorization", "")
		if res.status != http.StatusOK {
			t.Errorf("GET %s = %d, want it open", path, res.status)
		}
	}
	if got := decode[map[string]any](t, h.do(http.MethodGet, "/healthz", nil)); got["ok"] != true {
		t.Errorf("healthz = %v", got)
	}

	// Everything the parent app and the operator own is not, in either document.
	for _, path := range []string{"/sandboxes", "/hosts"} {
		if res := h.do(http.MethodGet, path, nil, "Authorization", ""); res.status != 401 {
			t.Errorf("GET %s without a token = %d", path, res.status)
		}
		res := h.do(http.MethodGet, path, nil, "Authorization", "Bearer nope")
		if res.status != 401 {
			t.Errorf("GET %s with the wrong token = %d", path, res.status)
		}
		// The two answers are the same sentence: the only fix for either is a different
		// token, so neither says which mistake was made.
		if got := decode[failure](t, res); got.Error != "unauthorized" {
			t.Errorf("GET %s body = %+v", path, got)
		}
	}
	if res := h.do(http.MethodGet, "/hosts", nil); res.status != 200 {
		t.Errorf("GET /hosts with the token = %d", res.status)
	}

	// A route in neither document falls past both generated routers to one 404.
	missing := h.do(http.MethodGet, "/nope", nil)
	if missing.status != 404 {
		t.Fatalf("unknown route = %d", missing.status)
	}
	if got := decode[failure](t, missing); got.Error != "route not found" {
		t.Errorf("body = %+v", got)
	}
}

// The owner is a credential that narrows the service token, so it is required on every
// route that scopes to one and there is no way to widen by leaving it out (decision 24).
func TestTheOwnerHeaderIsRequiredEverywhere(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/sandboxes", "/sandboxes/s_x"} {
		res := h.do(http.MethodGet, path, nil, "X-Sandboxd-Owner", "")
		if res.status != 400 {
			t.Errorf("GET %s with no owner = %d, want 400", path, res.status)
		}
		got := decode[failure](t, res)
		if len(got.Issues) != 1 || got.Issues[0].Path != "X-Sandboxd-Owner" {
			t.Errorf("GET %s issues = %+v, want the header named", path, got.Issues)
		}
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)

	empty := h.do(http.MethodPost, "/sandboxes", map[string]any{})
	if empty.status != 400 {
		t.Fatalf("empty body = %d", empty.status)
	}
	got := decode[failure](t, empty)
	var paths []string
	for _, i := range got.Issues {
		paths = append(paths, i.Path)
	}
	slices.Sort(paths)
	// The owner used to be a body field; it is a header now, so `image` is all that is left.
	if !slices.Equal(paths, []string{"image"}) {
		t.Errorf("issues = %v, want one entry per failing field", paths)
	}
	// The prose is the first problem, so a client with no interest in issues has something.
	if !strings.HasPrefix(got.Error, "image: ") {
		t.Errorf("error = %q", got.Error)
	}

	bad := map[string]struct {
		body any
		want string
	}{
		"a preset belongs to the client": {
			map[string]any{"image": "i", "repo": "x"}, "repo",
		},
		"an empty command is not a command": {
			map[string]any{"image": "i", "cmd": []string{}}, "cmd",
		},
		"below the idle floor": {
			map[string]any{"image": "i", "idle_timeout_s": 5}, "idle_timeout_s",
		},
		"a reserved variable": {
			map[string]any{"image": "i", "env": map[string]string{"TERM": "x"}}, "reserved",
		},
		"not a variable name": {
			map[string]any{"image": "i", "env": map[string]string{"bad-name": "x"}},
			"invalid variable name",
		},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			res := h.do(http.MethodPost, "/sandboxes", c.body)
			if res.status != 400 {
				t.Fatalf("= %d", res.status)
			}
			if got := decode[failure](t, res); !strings.Contains(got.Error, c.want) {
				t.Errorf("error = %q, want it to mention %q", got.Error, c.want)
			}
		})
	}
}

func TestSandboxLifecycle(t *testing.T) {
	h := newHarness(t)

	created := h.do(http.MethodPost, "/sandboxes", map[string]any{
		"image":      "img:1",
		"cmd":        []string{"bash", "-l"},
		"env":        map[string]string{"A": "1"},
		"secret_env": map[string]string{"S": "0-secret-0"},
	})
	if created.status != 201 {
		t.Fatalf("create = %d: %s", created.status, created.body)
	}
	v := decode[openapi.SandboxView](t, created)
	if v.Status != openapi.SandboxViewStatusQueued {
		t.Errorf("status = %s", v.Status)
	}
	if v.OwnerID != owner {
		t.Errorf("owner_id = %q, want the header's value", v.OwnerID)
	}
	if raw, _ := h.store.Dump("sandboxes"); strings.Contains(raw, "0-secret-0") {
		t.Errorf("a secret reached the database:\n%s", raw)
	}

	if list := decode[[]openapi.SandboxView](t, h.do(http.MethodGet, "/sandboxes", nil)); len(list) != 1 {
		t.Errorf("list = %d", len(list))
	}
	other := h.do(http.MethodGet, "/sandboxes", nil, "X-Sandboxd-Owner", "you")
	if list := decode[[]openapi.SandboxView](t, other); len(list) != 0 {
		t.Errorf("another owner sees %d", len(list))
	}

	if got := h.do(http.MethodGet, "/sandboxes/"+v.ID, nil).status; got != 200 {
		t.Errorf("GET one = %d", got)
	}
	if got := h.do(http.MethodGet, "/sandboxes/"+v.ID, nil, "X-Sandboxd-Owner", "you").status; got != 404 {
		t.Errorf("another owner's sandbox = %d, want it indistinguishable from missing", got)
	}

	// A terminal needs something to attach to.
	if got := h.do(http.MethodPost, "/sandboxes/"+v.ID+"/terminal", nil).status; got != 409 {
		t.Errorf("terminal on a queued sandbox = %d", got)
	}

	previewed := h.do(http.MethodPost, "/sandboxes/"+v.ID+"/preview", map[string]any{"port": 3000})
	if previewed.status != 201 {
		t.Fatalf("preview = %d: %s", previewed.status, previewed.body)
	}
	link := decode[openapi.Link](t, previewed)
	want := "https://3000-" + v.ID + ".preview.example.com/?t="
	if !strings.HasPrefix(link.URL, want) {
		t.Errorf("url = %q, want it to start %q", link.URL, want)
	}
	if link.ExpiresInS != 600 {
		t.Errorf("expires_in_s = %d", link.ExpiresInS)
	}

	// The port range is stated once, in the document, and the generated validator is what
	// enforces it — no use case sees an impossible port.
	outOfRange := h.do(http.MethodPost, "/sandboxes/"+v.ID+"/preview", map[string]any{"port": 70000})
	if outOfRange.status != 400 {
		t.Errorf("port 70000 = %d", outOfRange.status)
	}
	if got := decode[failure](t, outOfRange); len(got.Issues) != 1 || got.Issues[0].Path != "port" {
		t.Errorf("issues = %+v, want the field named", got.Issues)
	}

	ended := h.do(http.MethodDelete, "/sandboxes/"+v.ID, nil)
	if got := decode[openapi.SandboxView](t, ended); got.Status != openapi.SandboxViewStatusEnded {
		t.Errorf("delete = %s", got.Status)
	}
}

func TestTagsNoHostCanCarryAre422(t *testing.T) {
	h := newHarness(t)
	res := h.do(http.MethodPost, "/sandboxes", map[string]any{
		"image": "i", "tags": []string{"driver:kubernetes"},
	})
	// Not a 400: the request is well formed, the fleet just cannot ever serve it.
	if res.status != 422 {
		t.Fatalf("= %d, want 422", res.status)
	}
	if got := decode[failure](t, res); !strings.Contains(got.Error, "driver:kubernetes") {
		t.Errorf("error = %q", got.Error)
	}
}

func TestHosts(t *testing.T) {
	h := newHarness(t)
	err := h.store.InsertPendingHost(store.Host{
		ID: "h1", Name: "box", Fingerprint: "fp", ApproveCode: "ABCD-EF",
		MaxSandboxes: 2, Tags: []string{"driver:docker"},
	})
	if err != nil {
		t.Fatal(err)
	}

	codes := []struct {
		body any
		want int
		why  string
	}{
		{map[string]any{}, 400, "no code at all"},
		{map[string]any{"code": "nope"}, 400, "the wrong code"},
		// A mutation with nothing to say says it with the status (decision 25).
		{map[string]any{"code": "abcd-ef"}, 204, "the printed code, in any case"},
		{map[string]any{"code": "abcd-ef"}, 409, "a host that is no longer pending"},
	}
	for _, c := range codes {
		res := h.do(http.MethodPost, "/hosts/h1/approve", c.body)
		if res.status != c.want {
			t.Errorf("approve with %s = %d, want %d", c.why, res.status, c.want)
		}
		if res.status == 204 && len(res.body) != 0 {
			t.Errorf("a 204 carried a body: %s", res.body)
		}
	}
	if got := h.do(http.MethodPost, "/hosts/nope/revoke", nil).status; got != 404 {
		t.Errorf("revoke an unknown host = %d", got)
	}

	list := decode[[]adminapi.HostView](t, h.do(http.MethodGet, "/hosts", nil))
	if len(list) != 1 || list[0].Status != adminapi.HostViewStatusApproved {
		t.Fatalf("hosts = %+v", list)
	}
	if list[0].ApproveCode.Set {
		t.Error("the code is only shown while the host is pending")
	}
	if !slices.Equal(list[0].Tags, []string{"driver:docker"}) {
		t.Errorf("tags = %v", list[0].Tags)
	}
	if list[0].Online || list[0].Capacity.Set {
		t.Error("an offline host reports no capacity")
	}
}

// The document is a checked-in file now, so the control plane hands out the bytes it was
// generated from rather than a rendering of them. This is the one thing left to check:
// that what it serves is that file and not something else.
func TestTheDocumentsServedAreTheOnesGeneratedFrom(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string][]byte{
		"/openapi.yaml":       api.Client,
		"/openapi.admin.yaml": api.Admin,
	} {
		if got := h.do(http.MethodGet, path, nil).body; !bytes.Equal(got, want) {
			t.Errorf("GET %s served %d bytes, want the %d in the file", path, len(got), len(want))
		}
	}
}

// The terminal is the one operation in the document that the generated router does not
// serve. If net/http ever stops taking the route first, the handler that cannot upgrade a
// connection answers instead — so this asserts the socket's gate, not ogen's.
func TestTheTerminalSocketIsRoutedAheadOfTheGeneratedRouter(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ path, why string }{
		{"/sandboxes/s_x/terminal", "no token at all"},
		{"/sandboxes/s_x/terminal?token=nope", "a token we did not sign"},
	} {
		res := h.do(http.MethodGet, c.path, nil, "Authorization", "")
		if res.status != 401 {
			t.Errorf("%s = %d, want the socket's own 401", c.why, res.status)
		}
	}

	// A token for one sandbox is not a token for another, even though the signature is
	// ours either way.
	token := h.tokens.Sign(cp.TokenPayload{Kind: cp.TokenAttach, SID: "s_a"}, time.Minute)
	if got := h.do(http.MethodGet, "/sandboxes/s_b/terminal?token="+token, nil).status; got != 401 {
		t.Errorf("s_a's token on s_b's path = %d", got)
	}

	// The right token upgrades, and the bridge says why it is going away rather than
	// dropping the socket without a word.
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	good := h.tokens.Sign(cp.TokenPayload{Kind: cp.TokenAttach, SID: "s_missing"}, time.Minute)
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/sandboxes/s_missing/terminal?token=" + good
	// coder/websocket documents that the dial response body needs no closing.
	ws, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()

	typ, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("first frame = %v", typ)
	}
	var msg struct{ Type, Reason string }
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "closed" || !strings.Contains(msg.Reason, "not running") {
		t.Errorf("= %+v", msg)
	}
}
