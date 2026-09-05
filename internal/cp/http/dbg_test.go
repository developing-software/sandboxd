package http

import (
	"net/http"
	"testing"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
	"sandboxd/internal/wire"
)

func TestDebugDelete2(t *testing.T) {
	a := newAPI(t)
	w := a.connectWorker()

	v := decode[cp.SandboxView](t, a.do(http.MethodPost, "/sandboxes",
		map[string]any{"owner_id": "me", "image": "i"}))
	t.Logf("created %s status=%s", v.ID, v.Status)
	first := w.control()
	t.Logf("first: %T %+v", first, first)
	w.send(&wire.SandboxStarted{SID: v.ID})
	a.awaitStatus(v.ID, store.Running)

	ended := decode[cp.SandboxView](t,
		a.do(http.MethodDelete, "/sandboxes/"+v.ID+"?owner_id=me", nil))
	t.Logf("ended status %s", ended.Status)
	time.Sleep(50 * time.Millisecond)
	raw, _ := a.store.Dump("sandboxes")
	t.Logf("rows:\n%s", raw)
	second := w.control()
	t.Logf("second: %T %+v", second, second)
}
