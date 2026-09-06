package cp

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync"
	"testing"

	"sandboxd/internal/cp/store"
	"sandboxd/internal/gen/clientapi"
)

// countingStore counts the queue scans a listing costs. It delegates every call to the
// real store, so it is an observer rather than a stand-in.
type countingStore struct {
	*store.SQLite
	queued int
}

func (c *countingStore) Queued() ([]store.Sandbox, error) {
	c.queued++
	return c.SQLite.Queued()
}

func service(h *harness) *Sandboxes {
	cfg := Config{PublicURL: "https://cp.example.com", PreviewDomain: "preview.example.com"}
	return NewSandboxes(cfg, h.store, h.hub, h.sched, NewTokens("k"))
}

// env and secretEnv wrap a plain map in the optional the generated body declares. The
// constraints the document states — a present `image`, a non-empty `cmd`, a port in range
// — are checked by the generated decoder before a request reaches these use cases, so what
// is exercised here is only what is left: the semantics.
func env(m map[string]string) clientapi.OptCreateSandboxEnv {
	return clientapi.NewOptCreateSandboxEnv(m)
}

func secretEnv(m map[string]string) clientapi.OptCreateSandboxSecretEnv {
	return clientapi.NewOptCreateSandboxSecretEnv(m)
}

func TestCreateKeepsCallerEnvAndNeverStoresSecrets(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)

	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{
		Image:     "  img:1  ",
		Env:       env(map[string]string{"REPO": "https://x/r.git", "PROMPT": "p"}),
		SecretEnv: secretEnv(map[string]string{"GIT_TOKEN": "0-secret-0"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != clientapi.SandboxViewStatusQueued || v.QueuePosition.Null || v.QueuePosition.Value != 1 {
		t.Errorf("created = %+v", v)
	}
	if v.Image != "img:1" {
		t.Errorf("image = %q, want it trimmed", v.Image)
	}
	if !maps.Equal(v.Env, clientapi.SandboxViewEnv{"REPO": "https://x/r.git", "PROMPT": "p"}) {
		t.Errorf("env = %v", v.Env)
	}
	if v.IdleTimeoutS != DefaultIdleS {
		t.Errorf("defaults = %+v", v)
	}
	// Every list and map on a view is an empty one, never null: the document says so and
	// a generated client is typed on it.
	if v.Cmd == nil || len(v.Cmd) != 0 {
		t.Errorf("cmd = %v, want an empty list rather than null", v.Cmd)
	}
	if v.Tags == nil || len(v.Tags) != 0 {
		t.Errorf("tags = %v, want an empty list rather than null", v.Tags)
	}
	if raw := h.raw(); strings.Contains(raw, "0-secret-0") || strings.Contains(raw, "GIT_TOKEN") {
		t.Errorf("a secret reached the database:\n%s", raw)
	}
}

func TestCreateRejectsBadEnv(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	for _, c := range []*clientapi.CreateSandbox{
		{Image: "i", Env: env(map[string]string{"TERM": "x"})},
		{Image: "i", SecretEnv: secretEnv(map[string]string{"bad-name": "x"})},
	} {
		_, err := svc.Create(h.ctx, "me", c)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	}
	// Nothing was written on the way to the refusal.
	rows, err := h.store.Sandboxes("")
	if err != nil || len(rows) != 0 {
		t.Errorf("rows = %d, %v", len(rows), err)
	}
}

func TestOwnershipIsTheWholeModel(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{Image: "i"})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := svc.Get(v.ID, "me"); err != nil || got.ID != v.ID {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// Another owner's sandbox is indistinguishable from a missing one.
	if _, err := svc.Get(v.ID, "you"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another owner got %v", err)
	}
	// The header is required by the document, so this cannot arrive over HTTP — the check
	// is here because a use case that trusts the transport for ownership is a bug waiting.
	if _, err := svc.Get(v.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("no owner got %v", err)
	}

	if list, err := svc.List("me"); err != nil || len(list) != 1 {
		t.Errorf("list for the owner = %d, %v", len(list), err)
	}
	if list, err := svc.List("you"); err != nil || len(list) != 0 {
		t.Errorf("list for another owner = %d, %v", len(list), err)
	}

	ended, err := svc.Cancel(h.ctx, v.ID, "me")
	if err != nil || ended.Status != clientapi.SandboxViewStatusEnded {
		t.Errorf("cancel = %+v, %v", ended, err)
	}
}

func TestATerminalNeedsARunningSandbox(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{Image: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Terminal(v.ID, "me"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a queued sandbox has no terminal yet: %v", err)
	}

	// A host appears, the queue drains on its first heartbeat, and the sandbox starts.
	h.host("h1", 1)
	h.emit(Heartbeat{HostID: "h1"})
	h.emit(SandboxStarted{SID: v.ID})

	got, err := svc.Terminal(v.ID, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The link is the socket's own address with the credential already in it: the caller's
	// only move is to hand it to a browser (decision 26).
	want := "wss://cp.example.com/sandboxes/" + v.ID + "/terminal?token="
	if !strings.HasPrefix(got.URL, want) {
		t.Errorf("url = %q, want it to start %q", got.URL, want)
	}
	if got.ExpiresInS != 60 {
		t.Errorf("expires_in_s = %d", got.ExpiresInS)
	}
	if _, ok := NewTokens("k").Verify(tokenIn(t, got.URL, "token"), TokenAttach); !ok {
		t.Error("the token in the url does not verify")
	}
}

func TestPreviewURLCarriesItsOwnToken(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{Image: "i"})
	if err != nil {
		t.Fatal(err)
	}

	p, err := svc.Preview(v.ID, "me", 3000)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("https://3000-%s.preview.example.com/?t=", v.ID)
	if !strings.HasPrefix(p.URL, want) {
		t.Errorf("url = %q, want it to start %q", p.URL, want)
	}
	if p.ExpiresInS != 600 {
		t.Errorf("expires_in_s = %d", p.ExpiresInS)
	}
	payload, ok := NewTokens("k").Verify(tokenIn(t, p.URL, "t"), TokenPreview)
	if !ok || payload.SID != v.ID || payload.Port != 3000 {
		t.Errorf("token in the url = %+v, %v", payload, ok)
	}
	if _, err := svc.Preview(v.ID, "you", 3000); !errors.Is(err, ErrNotFound) {
		t.Errorf("another owner got %v", err)
	}
}

// tokenIn pulls a minted credential back out of the URL it was embedded in.
func tokenIn(t *testing.T, raw, param string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(param)
}

func TestTagsNoHostCanEverCarryAre422(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	h.host("docker", 1, "driver:docker", "arch:amd64")

	_, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{
		Image: "i", Tags: []string{"driver:kubernetes"},
	})
	if !errors.Is(err, ErrUnsatisfiable) {
		t.Fatalf("err = %v, want unsatisfiable rather than a sandbox queued forever", err)
	}
	if !strings.Contains(err.Error(), "driver:kubernetes") {
		t.Errorf("err = %q, want it to name the tag", err)
	}
	// Nothing was created: the request can never succeed, so there is nothing to wait for.
	rows, _ := h.store.Sandboxes("")
	if len(rows) != 0 {
		t.Errorf("rows = %d", len(rows))
	}

	// A tag the fleet does carry queues as usual, even with every host full.
	h.hub.setCapacity("docker", 1, 1)
	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{
		Image: "i", Tags: []string{"driver:docker"},
	})
	if err != nil {
		t.Fatalf("a busy fleet is not an unsatisfiable one: %v", err)
	}
	if v.Status != clientapi.SandboxViewStatusQueued {
		t.Errorf("status = %s", v.Status)
	}
}

func TestQueuePositionsComeFromOneSnapshot(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	var ids []string
	for range 3 {
		v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{Image: "i"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}

	list, err := svc.List("me")
	if err != nil {
		t.Fatal(err)
	}
	// Newest first in the response, oldest first in the queue.
	want := map[string]int{ids[0]: 1, ids[1]: 2, ids[2]: 3}
	for _, v := range list {
		if v.QueuePosition.Null || v.QueuePosition.Value != want[v.ID] {
			t.Errorf("%s position = %+v, want %d", v.ID, v.QueuePosition, want[v.ID])
		}
	}

	// One scan for the whole response, not one per queued row. This is a spy over the real
	// store, not a fake of it: every call still reaches SQLite.
	counted := &countingStore{SQLite: h.store}
	cfg := Config{PublicURL: "https://cp.example.com", PreviewDomain: "preview.example.com"}
	if _, err := NewSandboxes(cfg, counted, h.hub, h.sched, NewTokens("k")).List("me"); err != nil {
		t.Fatal(err)
	}
	if counted.queued != 1 {
		t.Errorf("listing %d sandboxes scanned the queue %d times", len(list), counted.queued)
	}

	// An ended sandbox has no position at all.
	if _, err := svc.Cancel(h.ctx, ids[0], "me"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ids[0], "me")
	if err != nil {
		t.Fatal(err)
	}
	if !got.QueuePosition.Null {
		t.Errorf("ended sandbox has position %d", got.QueuePosition.Value)
	}
	after, _ := svc.Get(ids[1], "me")
	if after.QueuePosition.Null || after.QueuePosition.Value != 1 {
		t.Errorf("the queue closed up: %+v", after.QueuePosition)
	}
}

func TestASandboxIsPlacedExactlyOnce(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	h.host("h1", 100)

	// Creates racing the drains a heartbeat triggers. If the row were written by the
	// handler rather than by the scheduler, a drain could see it queued before its
	// secrets were registered and place it a second time — with an empty secret_env.
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{
				Image:     "i",
				SecretEnv: secretEnv(map[string]string{"GIT_TOKEN": fmt.Sprintf("secret-%d", i)}),
			})
			if err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			h.sched.events <- Heartbeat{HostID: "h1"}
		}()
	}
	wg.Wait()
	// One more round trip, so every heartbeat queued behind the creates has been applied.
	if err := h.sched.do(h.ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, p := range h.hub.placements() {
		seen[p.spec.SID]++
		if len(p.spec.SecretEnv) != 1 {
			t.Errorf("%s was placed with secret_env %v", p.spec.SID, p.spec.SecretEnv)
		}
	}
	if len(seen) != n {
		t.Errorf("placed %d distinct sandboxes, want %d", len(seen), n)
	}
	for sid, times := range seen {
		if times != 1 {
			t.Errorf("%s was placed %d times", sid, times)
		}
	}
}

func TestHostOnlineIsLiveNotStored(t *testing.T) {
	h := newHarness(t, nil)
	svc := service(h)
	h.host("h1", 1)
	v, err := svc.Create(h.ctx, "me", &clientapi.CreateSandbox{Image: "i"})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := svc.Get(v.ID, "me")
	if got.HostOnline.Null || !got.HostOnline.Value {
		t.Fatalf("host_online = %+v, want true while the host is connected", got.HostOnline)
	}
	h.hub.offline("h1")
	got, _ = svc.Get(v.ID, "me")
	if got.HostOnline.Null || got.HostOnline.Value {
		t.Errorf("host_online = %+v after the host went away", got.HostOnline)
	}
}
