// Package http is the JSON API and the sockets beside it. The routing, the decoding and
// the validation are generated from the two documents in `api/`: this package supplies the
// two `Handler` implementations, the credential check, and the mapping from a use case's
// error to a status code. Nothing below it knows a status code.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"sandboxd/api"
	"sandboxd/internal/cp"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/gen/adminapi"
	"sandboxd/internal/gen/clientapi"
	"sandboxd/internal/wire"
)

// What the API needs. Each is the use-case surface of one package, declared here so this
// one stays a transport and nothing more.
type (
	sandboxAPI interface {
		Create(ctx context.Context, ownerID string, b *clientapi.CreateSandbox) (*clientapi.SandboxView, error)
		List(ownerID string) ([]clientapi.SandboxView, error)
		Get(sid, ownerID string) (*clientapi.SandboxView, error)
		Cancel(ctx context.Context, sid, ownerID string) (*clientapi.SandboxView, error)
		Terminal(sid, ownerID string) (*clientapi.Link, error)
		Preview(sid, ownerID string, port int) (*clientapi.Link, error)
	}
	hostAPI interface {
		List() ([]adminapi.HostView, error)
		Approve(id, code string) error
		Revoke(id string) error
	}
	attacher interface {
		Serve(w http.ResponseWriter, r *http.Request, sid string, size wire.Size)
	}
	previewer interface {
		Match(r *http.Request) (preview.Target, bool)
		ServeHTTP(w http.ResponseWriter, r *http.Request, t preview.Target)
	}
)

// Deps is what cmd/sandboxd-api hands over. It is a struct rather than seven arguments
// because every one of them is required and named.
type Deps struct {
	ServiceToken string
	Tokens       *cp.Tokens
	Sandboxes    sandboxAPI
	Hosts        hostAPI
	Attach       attacher
	Preview      previewer
	// Tunnel is the host hub's upgrade handler, served here because it shares the port.
	Tunnel http.HandlerFunc
	Log    *slog.Logger
}

// New builds the whole HTTP surface, preview hosts included.
//
// The two generated routers are chained rather than merged: the client's falls through to
// the admin's, and the admin's to the 404 below. Their paths are disjoint, so no request
// is ever offered to both — and neither document has to know the other exists, which is
// the point of there being two (DESIGN.md decision 27).
func New(d Deps) (http.Handler, error) {
	admin, err := adminapi.NewServer(
		&hostHandler{hosts: d.Hosts, log: d.Log},
		adminAuth{token: d.ServiceToken},
		adminapi.WithNotFound(notFound),
		adminapi.WithErrorHandler(adminErrors(d.Log)),
	)
	if err != nil {
		return nil, err
	}
	client, err := clientapi.NewServer(
		&sandboxHandler{sandboxes: d.Sandboxes, log: d.Log},
		clientAuth{token: d.ServiceToken},
		clientapi.WithNotFound(admin.ServeHTTP),
		clientapi.WithErrorHandler(clientErrors(d.Log)),
	)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// The four routes the generated servers do not serve. The terminal socket is in the
	// client document and is registered here first: an upgrade hijacks the connection, and
	// a generated handler is handed a context and params, not a ResponseWriter.
	mux.HandleFunc("GET /sandboxes/{id}/terminal", terminal(d.Tokens, d.Attach))
	mux.HandleFunc("GET /tunnel", d.Tunnel)
	mux.HandleFunc("GET /doc", docPage)
	mux.HandleFunc("GET /openapi.yaml", document(api.Client))
	mux.HandleFunc("GET /openapi.admin.yaml", document(api.Admin))
	mux.Handle("/", client)

	// The preview proxy matches on the Host header, so it runs before routing and before
	// any credential check: its own cookie is the credential there.
	return byHost(d.Preview, mux), nil
}

func byHost(p previewer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t, ok := p.Match(r); ok {
			p.ServeHTTP(w, r, t)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// terminal upgrades a browser onto a sandbox's PTY. The token in the query is the whole
// gate: a WebSocket carries neither the service token nor the owner header, and this token
// is short-lived and names one sandbox (decision 26).
func terminal(tokens *cp.Tokens, bridge attacher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		payload, ok := tokens.Verify(q.Get("token"), cp.TokenAttach)
		// The signature says the token is ours, not that it is for the sandbox in the
		// path. Serving A's terminal on B's URL would be answering a different question
		// than the one asked.
		if !ok || payload.SID != r.PathValue("id") {
			fail(w, http.StatusUnauthorized, "invalid or expired terminal token")
			return
		}
		bridge.Serve(w, r, payload.SID, wire.Size{
			Cols: positive(q.Get("cols"), 120),
			Rows: positive(q.Get("rows"), 40),
		})
	}
}

func positive(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// document serves one of the two contracts as the file it is. The control plane hands out
// the bytes it was generated from rather than a second rendering of them, which is the
// only way the served document and the checked-in one cannot disagree.
func document(doc []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(doc)
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	fail(w, http.StatusNotFound, "route not found")
}

// fail is for the handlers no generated server owns. They still speak its error shape.
func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func docPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head>
    <title>sandboxd API</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <div id="app"></div>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
    <script>
      Scalar.createApiReference('#app', {
        sources: [
          { url: '/openapi.yaml', title: 'Client' },
          { url: '/openapi.admin.yaml', title: 'Admin' },
        ],
      })
    </script>
  </body>
</html>`))
}
