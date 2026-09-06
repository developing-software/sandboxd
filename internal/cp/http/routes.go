// Package http is the JSON API and the two sockets in front of it. huma owns the
// routing, the validation and the OpenAPI document; the request and response structs in
// `cp` are the schema. Nothing below this package knows a status code.
package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/preview"
	"sandboxd/internal/wire"
)

// What the API needs. Each is the use-case surface of one package, declared here so this
// one stays a transport and nothing more.
type (
	sandboxAPI interface {
		Create(ctx context.Context, b cp.CreateSandbox) (cp.SandboxView, error)
		List(ownerID string) ([]cp.SandboxView, error)
		Get(sid, ownerID string) (cp.SandboxView, error)
		Cancel(ctx context.Context, sid, ownerID string) (cp.SandboxView, error)
		AttachToken(sid, ownerID string) (cp.AttachToken, error)
		PreviewToken(sid, ownerID string, port int) (cp.PreviewToken, error)
	}
	hostAPI interface {
		List() ([]cp.HostView, error)
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

// Deps is what cmd/sandboxd-api hands over. It is a struct rather than six arguments
// because every one of them is required and named.
type Deps struct {
	ServiceToken string
	PublicURL    string
	Tokens       *cp.Tokens
	Sandboxes    sandboxAPI
	Hosts        hostAPI
	Attach       attacher
	Preview      previewer
	// Tunnel is the host hub's upgrade handler, served here because it shares the port.
	Tunnel http.HandlerFunc
	Log    *slog.Logger
}

// Open to anyone: the probe, the document, the token-gated sockets, and the worker
// tunnel, which authenticates by fingerprint in its hello rather than by bearer.
var public = map[string]struct{}{
	"/healthz":      {},
	"/attach":       {},
	"/tunnel":       {},
	"/doc":          {},
	"/openapi.json": {},
	"/openapi.yaml": {},
}

// New builds the whole HTTP surface, preview hosts included.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	register(mux, d.PublicURL, d.Sandboxes, d.Hosts, d.Log)

	mux.HandleFunc("GET /tunnel", d.Tunnel)
	mux.HandleFunc("GET /attach", attachHandler(d.Tokens, d.Attach))
	mux.HandleFunc("GET /doc", docPage)
	mux.HandleFunc("/", notFound)

	// The preview proxy matches on the Host header, so it runs before routing and before
	// the bearer check: its own cookie is the credential there.
	return byHost(d.Preview, bearer(d.ServiceToken, mux))
}

// register puts every operation on one huma API. Spec calls it too, with no use cases
// behind it, so the document and the running server can never describe different routes.
func register(mux *http.ServeMux, publicURL string, s sandboxAPI, h hostAPI, log *slog.Logger) huma.API {
	useHumaGlobals()
	api := humago.New(mux, config(publicURL))
	registerSandboxes(api, s, log)
	registerHosts(api, h, log)
	registerService(api)
	documentAttach(api.OpenAPI())
	nullableEnums(api.OpenAPI().Components.Schemas)
	return api
}

// nullableEnums repairs the one shape huma renders as a contradiction: a nullable field
// carrying an enum lists its values but not `null`, so the enum forbids the very value the
// type allows. A generator believes the enum, and `SandboxView.ended_reason` — null until
// the sandbox ends — would reach a client typed as always present.
func nullableEnums(reg huma.Registry) {
	for _, schema := range reg.Map() {
		for _, prop := range schema.Properties {
			if prop.Nullable && len(prop.Enum) > 0 && !slices.Contains(prop.Enum, nil) {
				prop.Enum = append(prop.Enum, nil)
			}
		}
	}
}

func config(publicURL string) huma.Config {
	cfg := huma.DefaultConfig("sandboxd control plane", "1.0.0")
	cfg.Info.Description = "Generic sandboxes on your own hosts. " +
		"Presets, repos and agents are a client concern and reach this API as an image, " +
		"a command, env and tags."
	// Our own page, at the path the TypeScript control plane served it from.
	cfg.DocsPath = ""
	// huma's only create hook hangs a `$schema` link off every body it renders. Dropping
	// it keeps the document free of a field no caller sends and no generator should type.
	cfg.CreateHooks = nil
	cfg.Servers = []*huma.Server{{URL: publicURL}}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"Bearer": {Type: "http", Scheme: "bearer"},
	}
	cfg.Security = []map[string][]string{{"Bearer": {}}}
	return cfg
}

// bearer gates everything the parent app owns. The few public paths are listed above.
func bearer(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := public[r.URL.Path]; ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != expected {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
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

// attachHandler upgrades a browser to a sandbox's terminal. Its token is the gate, which
// is why the path is public.
func attachHandler(tokens *cp.Tokens, bridge attacher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		payload, ok := tokens.Verify(q.Get("token"), cp.TokenAttach)
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid or expired attach token")
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

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "route not found")
}

// writeError is for the handlers huma does not own. They still speak its error shape.
func writeError(w http.ResponseWriter, status int, msg string) {
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
    <script>Scalar.createApiReference('#app', { url: '/openapi.json' })</script>
  </body>
</html>`))
}
