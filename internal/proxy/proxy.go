// Package proxy implements the HTTP handler for hindsight-auth-proxy.
//
// Request flow:
//  1. GET /healthz → 200; no identity required.
//  2. Resolve caller identity via WhoIs (tsnet) or a dev header.
//  3. Parse bank_id from the URL path; unscoped paths require admin.
//  4. Check the ACL; deny with 403 on failure.
//  5. Forward to the upstream Hindsight instance, injecting the bearer token.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"main/internal/authz"
	"main/internal/logger"
)

// WhoIsFunc resolves the caller's email from their remote address.
// Returns ("", nil) or an error when identity cannot be determined.
// Nil in dev mode — the handler reads the identity from DevIdentityHdr instead.
type WhoIsFunc func(ctx context.Context, addr string) (string, error)

// DialFunc is the signature of tsnet.Server.Dial, used as the transport's
// DialContext so connections to the upstream go through the tailnet.
// Nil in dev mode — the default http.Transport is used (upstream reachable via localhost).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Options configures the proxy handler. Deliberately a plain struct (no *config.Config
// dependency) so the proxy package is importable in tests without triggering config init.
type Options struct {
	// UpstreamURL is the base URL of the upstream Hindsight instance.
	// In production it is a tailnet MagicDNS address; in dev it is localhost.
	UpstreamURL string

	// UpstreamToken is the bearer secret injected into every upstream request.
	// Must match HINDSIGHT_API_TENANT_API_KEY / HINDSIGHT_API_MCP_AUTH_TOKEN on Hindsight.
	UpstreamToken string

	// DevIdentityHdr is the HTTP header name from which the caller's email is read
	// in dev mode (when WhoIs is nil). Empty in production.
	DevIdentityHdr string
}

// Handler is an http.Handler that enforces Tailscale identity + bank ACL before
// proxying to the upstream Hindsight instance.
type Handler struct {
	opts  Options
	acl   atomic.Pointer[authz.ACL]
	whoIs WhoIsFunc // nil in dev mode
	rp    *httputil.ReverseProxy
}

// New constructs a Handler.
//   - whoIs: nil in dev mode; otherwise wraps ts.LocalClient().WhoIs.
//   - dial:  nil in dev mode; otherwise ts.Dial for tailnet transport.
func New(opts Options, acl *authz.ACL, whoIs WhoIsFunc, dial DialFunc) *Handler {
	upstreamURL, err := url.Parse(opts.UpstreamURL)
	if err != nil {
		// Upstream URL is validated by config.init before New is called;
		// a panic here is a programming error.
		panic(fmt.Sprintf("invalid UpstreamURL %q: %v", opts.UpstreamURL, err))
	}

	rp := httputil.NewSingleHostReverseProxy(upstreamURL)
	rp.FlushInterval = 0 // 0 = no periodic flush; SSE is auto-detected by
	// httputil.ReverseProxy.flushInterval() via text/event-stream content-type
	// and overrides this with -1 automatically. For non-SSE responses this
	// avoids flushing on every io.Copy write, which causes small repeated
	// writes to tsnet's userspace TCP and severely limits throughput.

	token := opts.UpstreamToken
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		// Overwrite any client-supplied Authorization; inject the upstream secret.
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Clone DefaultTransport so we never mutate the global.
	// DisableCompression stops the transport from advertising Accept-Encoding: gzip;
	// without it, FastAPI's GZipMiddleware compresses responses and strips
	// Content-Length, causing the reverse proxy to re-encode the body as chunked.
	// Chunked bodies stall when written over tsnet's userspace TCP stack — the same
	// path that works fine on kernel TCP. Keeping the response uncompressed
	// preserves the upstream Content-Length, which tsnet handles correctly.
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableCompression = true
	if dial != nil {
		t.DialContext = dial
	}
	rp.Transport = t

	h := &Handler{
		opts:  opts,
		whoIs: whoIs,
		rp:    rp,
	}
	h.acl.Store(acl)
	return h
}

// SetACL atomically replaces the active ACL. Called from the SIGHUP handler in main.
func (h *Handler) SetACL(a *authz.ACL) {
	h.acl.Store(a)
}

// ServeHTTP handles an incoming request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Health check — no identity required (ops/load-balancer probe).
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// 2. Resolve identity.
	email, err := h.resolveIdentity(r)
	if err != nil {
		logger.Stdout.Warn("identity resolution error",
			slog.String("remote", r.RemoteAddr),
			slog.Any("error", err),
		)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if email == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 3. Bank ACL.
	acl := h.acl.Load()
	bankID, scoped := authz.BankFromPath(r.URL.Path)

	if !scoped {
		// Unscoped path (root, /metrics, /docs, bank list, etc.).
		// Allow only admins to prevent non-admin enumeration.
		if !acl.IsAdmin(email) {
			logger.Stdout.Info("unscoped path denied",
				slog.String("email", email),
				slog.String("path", r.URL.Path),
			)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	} else if !acl.Allowed(email, bankID) {
		logger.Stdout.Info("bank access denied",
			slog.String("email", email),
			slog.String("bank", bankID),
			slog.String("path", r.URL.Path),
		)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	logger.Stdout.Info("proxying request",
		slog.String("email", email),
		slog.String("bank", bankID),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	)

	// 4. Forward to Hindsight.
	h.rp.ServeHTTP(w, r)
}

// resolveIdentity returns the caller's email (lowercase, trimmed).
// In dev mode it reads from opts.DevIdentityHdr; in production it calls WhoIs.
func (h *Handler) resolveIdentity(r *http.Request) (string, error) {
	var raw string

	if h.opts.DevIdentityHdr != "" {
		raw = r.Header.Get(h.opts.DevIdentityHdr)
	} else if h.whoIs != nil {
		var err error
		raw, err = h.whoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			return "", err
		}
	}

	return strings.ToLower(strings.TrimSpace(raw)), nil
}
