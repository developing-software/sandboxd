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

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/attach"
	"sandboxd/internal/cp/hosts"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/cp/store"
)

// The whole control plane behind an httptest.Server, with no worker connected: every
// dependency is the real one, which is what makes the status codes worth asserting.

const serviceToken = "secret"

type api struct {
	t      *testing.T
	ctx    context.Context
	srv    *httptest.Server
	store  *store.SQLite
	hub    *hosts.Hub
	tokens *cp.Tokens
	svc    *cp.Sandboxes
}

func newAPI(t *testing.T) *api {
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
	srv := httptest.NewServer(New(Deps{
		ServiceToken: cfg.ServiceToken,
		PublicURL:    cfg.PublicURL,
		Tokens:       tokens,
		Sandboxes:    svc,
		Hosts:        hosts.NewService(st, hub, log),
		Attach:       attach.NewBridge(svc, hub, log),
		Preview:      preview.NewProxy(svc, hub, tokens, cfg.PreviewDomain, log),
		Tunnel:       hub.Serve,
		Log:          log,
	}))
	t.Cleanup(srv.Close)

	return &api{t: t, ctx: ctx, srv: srv, store: st, hub: hub, tokens: tokens, svc: svc}
}

// response is a finished exchange: the body is already read and the socket returned to
// the pool, so a test never has one open.
type response struct {
	path   string
	status int
	body   []byte
}

// do sends a request with the service token unless told otherwise.
func (a *api) do(method, path string, body any, headers ...string) response {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(a.ctx, method, a.srv.URL+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
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
	a := newAPI(t)

	// The probe and the document are open.
	for _, path := range []string{"/healthz", "/openapi.json", "/doc"} {
		res := a.do(http.MethodGet, path, nil, "Authorization", "")
		if res.status != http.StatusOK {
			t.Errorf("GET %s = %d, want it open", path, res.status)
		}
	}
	if got := decode[map[string]any](t, a.do(http.MethodGet, "/healthz", nil)); got["ok"] != true {
		t.Errorf("healthz = %v", got)
	}

	// Everything the parent app owns is not.
	for _, path := range []string{"/sandboxes", "/hosts"} {
		if res := a.do(http.MethodGet, path, nil, "Authorization", ""); res.status != 401 {
			t.Errorf("GET %s without a token = %d", path, res.status)
		}
		res := a.do(http.MethodGet, path, nil, "Authorization", "Bearer nope")
		if res.status != 401 {
			t.Errorf("GET %s with the wrong token = %d", path, res.status)
		}
	}
	if res := a.do(http.MethodGet, "/hosts", nil); res.status != 200 {
		t.Errorf("GET /hosts with the token = %d", res.status)
	}

	missing := a.do(http.MethodGet, "/nope", nil)
	if missing.status != 404 {
		t.Fatalf("unknown route = %d", missing.status)
	}
	if got := decode[failure](t, missing); got.Error != "route not found" {
		t.Errorf("body = %+v", got)
	}
}

func TestCreateValidation(t *testing.T) {
	a := newAPI(t)

	empty := a.do(http.MethodPost, "/sandboxes", map[string]any{})
	if empty.status != 400 {
		t.Fatalf("empty body = %d, want 400 rather than huma's 422", empty.status)
	}
	got := decode[failure](t, empty)
	var paths []string
	for _, i := range got.Issues {
		paths = append(paths, i.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"image", "owner_id"}) {
		t.Errorf("issues = %v, want one entry per failing field", paths)
	}
	// The prose is the first problem, so a client with no interest in issues has something.
	if !strings.HasPrefix(got.Error, "image: ") && !strings.HasPrefix(got.Error, "owner_id: ") {
		t.Errorf("error = %q", got.Error)
	}

	bad := map[string]struct {
		body any
		want string
	}{
		"a preset belongs to the client": {
			map[string]any{"owner_id": "me", "image": "i", "repo": "x"}, "repo",
		},
		"an empty command is not a command": {
			map[string]any{"owner_id": "me", "image": "i", "cmd": []string{}}, "cmd",
		},
		"below the idle floor": {
			map[string]any{"owner_id": "me", "image": "i", "idle_timeout_s": 5}, "idle_timeout_s",
		},
		"a reserved variable": {
			map[string]any{"owner_id": "me", "image": "i", "env": map[string]string{"TERM": "x"}},
			"reserved",
		},
		"not a variable name": {
			map[string]any{"owner_id": "me", "image": "i", "env": map[string]string{"bad-name": "x"}},
			"invalid variable name",
		},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			res := a.do(http.MethodPost, "/sandboxes", c.body)
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
	a := newAPI(t)

	created := a.do(http.MethodPost, "/sandboxes", map[string]any{
		"owner_id":   "me",
		"image":      "img:1",
		"cmd":        []string{"bash", "-l"},
		"env":        map[string]string{"A": "1"},
		"secret_env": map[string]string{"S": "0-secret-0"},
	})
	if created.status != 201 {
		t.Fatalf("create = %d", created.status)
	}
	v := decode[cp.SandboxView](t, created)
	if v.Status != store.Queued {
		t.Errorf("status = %s", v.Status)
	}
	if raw, _ := a.store.Dump("sandboxes"); strings.Contains(raw, "0-secret-0") {
		t.Errorf("a secret reached the database:\n%s", raw)
	}

	if list := decode[[]cp.SandboxView](t, a.do(http.MethodGet, "/sandboxes?owner_id=me", nil)); len(list) != 1 {
		t.Errorf("list = %d", len(list))
	}
	if list := decode[[]cp.SandboxView](t, a.do(http.MethodGet, "/sandboxes?owner_id=you", nil)); len(list) != 0 {
		t.Errorf("another owner sees %d", len(list))
	}

	codes := map[string]int{
		"/sandboxes/" + v.ID + "?owner_id=me":  200,
		"/sandboxes/" + v.ID + "?owner_id=you": 404,
		"/sandboxes/" + v.ID:                   400, // owner_id is not optional here
	}
	for path, want := range codes {
		if got := a.do(http.MethodGet, path, nil).status; got != want {
			t.Errorf("GET %s = %d, want %d", path, got, want)
		}
	}

	// A terminal needs something to attach to.
	attachRes := a.do(http.MethodPost, "/sandboxes/"+v.ID+"/attach-token", map[string]any{"owner_id": "me"})
	if attachRes.status != 409 {
		t.Errorf("attach-token on a queued sandbox = %d", attachRes.status)
	}

	previewRes := a.do(http.MethodPost, "/sandboxes/"+v.ID+"/preview-token",
		map[string]any{"owner_id": "me", "port": 3000})
	if previewRes.status != 200 {
		t.Fatalf("preview-token = %d", previewRes.status)
	}
	p := decode[cp.PreviewToken](t, previewRes)
	want := "https://3000-" + v.ID + ".preview.example.com/?t=" + p.Token
	if p.URL != want {
		t.Errorf("url = %q, want %q", p.URL, want)
	}
	outOfRange := a.do(http.MethodPost, "/sandboxes/"+v.ID+"/preview-token",
		map[string]any{"owner_id": "me", "port": 70000})
	if outOfRange.status != 400 {
		t.Errorf("port 70000 = %d", outOfRange.status)
	}

	ended := a.do(http.MethodDelete, "/sandboxes/"+v.ID+"?owner_id=me", nil)
	if got := decode[cp.SandboxView](t, ended); got.Status != store.Ended {
		t.Errorf("delete = %s", got.Status)
	}
}

func TestTagsNoHostCanCarryAre422(t *testing.T) {
	a := newAPI(t)
	res := a.do(http.MethodPost, "/sandboxes", map[string]any{
		"owner_id": "me", "image": "i", "tags": []string{"driver:kubernetes"},
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
	a := newAPI(t)
	err := a.store.InsertPendingHost(store.Host{
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
		{map[string]any{"code": "abcd-ef"}, 200, "the printed code, in any case"},
		{map[string]any{"code": "abcd-ef"}, 409, "a host that is no longer pending"},
	}
	for _, c := range codes {
		got := a.do(http.MethodPost, "/hosts/h1/approve", c.body).status
		if got != c.want {
			t.Errorf("approve with %s = %d, want %d", c.why, got, c.want)
		}
	}
	if got := a.do(http.MethodPost, "/hosts/nope/revoke", nil).status; got != 404 {
		t.Errorf("revoke an unknown host = %d", got)
	}

	list := decode[[]cp.HostView](t, a.do(http.MethodGet, "/hosts", nil))
	if len(list) != 1 || list[0].Status != store.HostApproved {
		t.Fatalf("hosts = %+v", list)
	}
	if list[0].ApproveCode != "" {
		t.Error("the code is only shown while the host is pending")
	}
	if !slices.Equal(list[0].Tags, []string{"driver:docker"}) {
		t.Errorf("tags = %v", list[0].Tags)
	}
	if list[0].Online || list[0].Capacity != nil {
		t.Error("an offline host reports no capacity")
	}
}

func TestTheDocumentListsEveryRoute(t *testing.T) {
	a := newAPI(t)
	doc := decode[struct {
		Paths map[string]any `json:"paths"`
	}](t, a.do(http.MethodGet, "/openapi.json", nil))

	got := slices.Sorted(maps(doc.Paths))
	want := []string{
		"/attach",
		"/healthz",
		"/hosts",
		"/hosts/{id}/approve",
		"/hosts/{id}/revoke",
		"/sandboxes",
		"/sandboxes/{id}",
		"/sandboxes/{id}/attach-token",
		"/sandboxes/{id}/preview-token",
	}
	if !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestAttachIsGatedByItsOwnToken(t *testing.T) {
	a := newAPI(t)
	if got := a.do(http.MethodGet, "/attach", nil, "Authorization", "").status; got != 401 {
		t.Errorf("no token = %d", got)
	}
	if got := a.do(http.MethodGet, "/attach?token=nope", nil).status; got != 401 {
		t.Errorf("a token we did not sign = %d", got)
	}

	// A good token upgrades, and the bridge says why it is going away rather than
	// dropping the socket without a word.
	token := a.tokens.Sign(cp.TokenPayload{Kind: cp.TokenAttach, SID: "s_missing"}, time.Minute)
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/attach?token=" + token
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

func maps(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
