// Package preview proxies https://<port>-<sid>.<domain>/... into a port inside a
// sandbox. The whole of it rests on the tunnel stream being a net.Conn: ReverseProxy
// dials one and then does HTTP, streamed bodies and WebSocket upgrades itself.
package preview

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sandboxd/internal/cp"
	"sandboxd/internal/cp/store"
)

// Cookie is what the first request trades its preview token for.
const Cookie = "sandboxd_preview"

const cookieTTL = 12 * time.Hour

type (
	sandboxes interface {
		Sandbox(sid string) (store.Sandbox, bool, error)
	}
	dialer interface {
		Online(hostID string) bool
		Dial(ctx context.Context, hostID, sid string, port int) (net.Conn, error)
	}
)

// Target is which sandbox and port a preview host names.
type Target struct {
	SID    string
	Port   int
	hostID string
}

type ctxKey struct{}

type Proxy struct {
	store  sandboxes
	hub    dialer
	tokens *cp.Tokens
	domain string
	host   *regexp.Regexp
	rp     *httputil.ReverseProxy
	log    *slog.Logger
}

func NewProxy(s sandboxes, hub dialer, tokens *cp.Tokens, domain string, log *slog.Logger) *Proxy {
	p := &Proxy{
		store: s, hub: hub, tokens: tokens, domain: domain, log: log,
		host: regexp.MustCompile(
			`^(?i)(\d{1,5})-(s_[a-z2-7]+)\.` + regexp.QuoteMeta(domain) + `(?::\d+)?$`),
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:      p.rewrite,
		ErrorHandler: p.onError,
		// Flush every write straight through: a dev server's live reload and a notebook's
		// long-poll are the normal traffic here, not documents.
		FlushInterval: -1,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				t, ok := ctx.Value(ctxKey{}).(Target)
				if !ok {
					return nil, fmt.Errorf("preview: no target on the request")
				}
				return p.hub.Dial(ctx, t.hostID, t.SID, t.Port)
			},
			// One hop over a stream we already own: compression and connection ceremony
			// buy nothing, and the sandbox is not on a network.
			DisableCompression:  true,
			MaxIdleConnsPerHost: 8,
		},
	}
	return p
}

// Match reports whether this request is for a preview host rather than the JSON API.
func (p *Proxy) Match(r *http.Request) (Target, bool) {
	m := p.host.FindStringSubmatch(r.Host)
	if m == nil {
		return Target{}, false
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port < 1 || port > 65535 {
		return Target{}, false
	}
	return Target{SID: strings.ToLower(m[2]), Port: port}, true
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request, t Target) {
	// First visit: ?t=<preview token> becomes a cookie and a redirect to the clean URL,
	// so the token does not stay in the address bar or in the app's own links.
	if token := r.URL.Query().Get("t"); token != "" {
		p.exchange(w, r, t, token)
		return
	}
	cookie, err := r.Cookie(Cookie)
	if err != nil {
		http.Error(w, "preview requires a token: open the link from the app", http.StatusUnauthorized)
		return
	}
	payload, ok := p.tokens.Verify(cookie.Value, cp.TokenPreviewCookie)
	if !ok || payload.SID != t.SID || payload.Port != t.Port {
		http.Error(w, "preview requires a token: open the link from the app", http.StatusUnauthorized)
		return
	}

	sb, found, err := p.store.Sandbox(t.SID)
	if err != nil {
		http.Error(w, "sandbox lookup failed", http.StatusBadGateway)
		return
	}
	// A sandbox that is not running is a 502, not a 404: the URL is valid, the sandbox is
	// not there (DESIGN.md decision 11).
	if !found || sb.Status != store.Running || sb.HostID == nil {
		http.Error(w, "sandbox is not running", http.StatusBadGateway)
		return
	}
	if !p.hub.Online(*sb.HostID) {
		http.Error(w, "host is offline", http.StatusBadGateway)
		return
	}
	t.hostID = *sb.HostID
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
}

func (p *Proxy) exchange(w http.ResponseWriter, r *http.Request, t Target, token string) {
	payload, ok := p.tokens.Verify(token, cp.TokenPreview)
	if !ok || payload.SID != t.SID || payload.Port != t.Port {
		http.Error(w, "invalid or expired preview token", http.StatusUnauthorized)
		return
	}
	clean := *r.URL
	q := clean.Query()
	q.Del("t")
	clean.RawQuery = q.Encode()

	value := p.tokens.Sign(
		cp.TokenPayload{Kind: cp.TokenPreviewCookie, SID: t.SID, Port: t.Port}, cookieTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     Cookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cookieTTL.Seconds()),
		Secure:   secure(r),
	})
	http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	t, _ := pr.In.Context().Value(ctxKey{}).(Target)

	pr.Out.URL.Scheme = "http"
	// The pool key, and only that: http.Transport keys idle connections by scheme+host,
	// so one shared transport would otherwise hand a stream dialled for one sandbox to a
	// request for another.
	pr.Out.URL.Host = fmt.Sprintf("%s-%d", t.SID, t.Port)
	// What the app inside the container sees, which is where it actually is.
	pr.Out.Host = fmt.Sprintf("localhost:%d", t.Port)

	// A front proxy's own headers win, because it is the one that knows how the browser
	// reached us; SetXForwarded would otherwise replace them with what we can see.
	proto := pr.In.Header.Get("X-Forwarded-Proto")
	host := pr.In.Header.Get("X-Forwarded-Host")
	pr.SetXForwarded()
	if proto != "" {
		pr.Out.Header.Set("X-Forwarded-Proto", proto)
	}
	if host != "" {
		pr.Out.Header.Set("X-Forwarded-Host", host)
	}
	stripCookie(pr.Out)
}

func (p *Proxy) onError(w http.ResponseWriter, r *http.Request, err error) {
	t, _ := r.Context().Value(ctxKey{}).(Target)
	p.log.Warn("preview upstream", "sid", t.SID, "port", t.Port, "err", err)
	http.Error(w,
		fmt.Sprintf("could not reach port %d in the sandbox: %v", t.Port, err),
		http.StatusBadGateway)
}

// stripCookie keeps our own cookie out of the sandbox: it is a capability for this proxy,
// and an app inside the container has no business reading it.
func stripCookie(r *http.Request) {
	cookies := r.Cookies()
	kept := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name != Cookie {
			kept = append(kept, c.String())
		}
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// secure decides whether the cookie may be marked Secure, which it must not be when the
// browser reached us over plain http — the cookie would simply never come back.
func secure(r *http.Request) bool {
	return r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		strings.EqualFold(r.URL.Scheme, "https")
}
