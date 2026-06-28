package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"main/internal/authz"
	"main/internal/proxy"
)

// ── test fixtures ─────────────────────────────────────────────────────────────

const testACLYAML = `
admins:
  - richard@brickeye.com
shared:
  - org-*
teams:
  sw:
    banks: [team-sw-*]
    members: [alice@brickeye.com, bob@brickeye.com]
  rnd:
    banks: [team-rnd-*]
    members: [carol@brickeye.com]
users:
  alice@brickeye.com:
    banks: [hermes-alice, scratch-alice-*]
  bob@brickeye.com:
    banks: [hermes-bob]
  carol@brickeye.com:
    banks: [hermes-carol]
`

func loadACL(t *testing.T) *authz.ACL {
	t.Helper()
	f := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(f, []byte(testACLYAML), 0o600); err != nil {
		t.Fatalf("write acl: %v", err)
	}
	a, err := authz.Load(f)
	if err != nil {
		t.Fatalf("Load ACL: %v", err)
	}
	return a
}

// upstreamSpy is a test upstream that records the last request it received.
type upstreamSpy struct {
	mu          sync.Mutex
	lastAuth    string
	lastPath    string
	callCount   int
	statusCode  int // response status to return (default 200)
}

func (s *upstreamSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lastAuth = r.Header.Get("Authorization")
		s.lastPath = r.URL.Path
		s.callCount++
		code := s.statusCode
		s.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	})
}

func (s *upstreamSpy) reset() {
	s.mu.Lock()
	s.lastAuth = ""
	s.lastPath = ""
	s.callCount = 0
	s.mu.Unlock()
}

// newHandler creates a Handler in dev mode with X-Dev-User as the identity header.
// The upstream is the provided httptest.Server.
func newHandler(t *testing.T, upstream *httptest.Server, acl *authz.ACL) *proxy.Handler {
	t.Helper()
	return proxy.New(proxy.Options{
		UpstreamURL:    upstream.URL,
		UpstreamToken:  "secret-upstream-token",
		DevIdentityHdr: "X-Dev-User",
	}, acl, nil, nil) // nil whoIs + nil dial: dev mode
}

func get(h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func post(h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// ── tests ─────────────────────────────────────────────────────────────────────

// AC: GET /healthz → 200, no identity required.
func TestHealthz_NoAuth_200(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/healthz", nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 0 {
		t.Errorf("healthz must not reach upstream; got %d upstream calls", calls)
	}
}

// AC: identity absent → 401.
func TestNoIdentityHeader_401(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-alice/", nil) // no X-Dev-User header

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 0 {
		t.Errorf("unauthenticated request must not reach upstream; got %d upstream calls", calls)
	}
}

// AC: empty identity header → 401.
func TestEmptyIdentityHeader_401(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-alice/", map[string]string{"X-Dev-User": "  "})

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// AC: WhoIs error → 401 (production mode: no DevIdentityHdr, whoIs returns error).
func TestWhoIsError_401(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()

	// Production mode: DevIdentityHdr is empty, so resolveIdentity calls whoIs.
	h := proxy.New(proxy.Options{
		UpstreamURL:   upstream.URL,
		UpstreamToken: "secret",
		// DevIdentityHdr intentionally empty → production path through whoIs.
	}, loadACL(t), func(_ context.Context, addr string) (string, error) {
		return "", errors.New("tailscale: node not found")
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/mcp/hermes-alice/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// AC: alice → hermes-alice → 200 (user grant, exact match).
func TestAllowedBank_UserGrant_200(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-alice/", map[string]string{"X-Dev-User": "alice@brickeye.com"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 1 {
		t.Errorf("expect exactly 1 upstream call, got %d", calls)
	}
}

// AC: alice → hermes-bob → 403 (another user's private bank).
func TestDeniedBank_OtherUser_403(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-bob/", map[string]string{"X-Dev-User": "alice@brickeye.com"})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 0 {
		t.Errorf("denied request must not reach upstream; got %d upstream calls", calls)
	}
}

// AC: alice → team-sw-roadmap → 200 (team grant, glob match).
func TestAllowedBank_TeamGrant_200(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/team-sw-roadmap/", map[string]string{"X-Dev-User": "alice@brickeye.com"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

// AC: carol → team-sw-roadmap → 403 (carol is in rnd, not sw).
func TestDeniedBank_WrongTeam_403(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/team-sw-roadmap/", map[string]string{"X-Dev-User": "carol@brickeye.com"})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

// AC: anyone (dave, not in ACL) → org-handbook → 200 (shared bank).
func TestAllowedBank_SharedOrg_AnyAuthedUser_200(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/org-handbook/", map[string]string{"X-Dev-User": "dave@brickeye.com"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

// AC: non-admin → /mcp/ (unscoped, no bank id) → 403.
func TestUnscopedPath_NonAdmin_403(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	paths := []string{"/mcp/", "/", "/metrics", "/docs", "/v1/default/banks"}
	for _, p := range paths {
		spy.reset()
		rr := get(h, p, map[string]string{"X-Dev-User": "alice@brickeye.com"})
		if rr.Code != http.StatusForbidden {
			t.Errorf("path %q: want 403, got %d", p, rr.Code)
		}
		spy.mu.Lock()
		calls := spy.callCount
		spy.mu.Unlock()
		if calls != 0 {
			t.Errorf("path %q: denied unscoped must not reach upstream; got %d upstream calls", p, calls)
		}
	}
}

// AC: admin (richard) → /mcp/ (unscoped) → proxied through.
func TestUnscopedPath_Admin_Proxied(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/", map[string]string{"X-Dev-User": "richard@brickeye.com"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 1 {
		t.Errorf("admin unscoped path must reach upstream; got %d upstream calls", calls)
	}
}

// AC: upstream receives "Authorization: Bearer <token>"; client-supplied header is replaced.
func TestBearerTokenInjected(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	// Client sends a different auth header; it must be overwritten.
	req := httptest.NewRequest(http.MethodGet, "/mcp/hermes-alice/", nil)
	req.Header.Set("X-Dev-User", "alice@brickeye.com")
	req.Header.Set("Authorization", "Bearer client-supplied-token-should-be-replaced")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	spy.mu.Lock()
	auth := spy.lastAuth
	spy.mu.Unlock()
	want := "Bearer secret-upstream-token"
	if auth != want {
		t.Errorf("upstream Authorization = %q, want %q", auth, want)
	}
}

// AC: SIGHUP ACL hot-reload — SetACL atomically swaps the policy.
// Prove: a bank denied under the old ACL becomes allowed after SetACL.
func TestACLHotReload_SetACL(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()

	// Initial ACL: dave has no personal banks.
	h := newHandler(t, upstream, loadACL(t))

	// dave → hermes-dave: denied under initial ACL.
	rr := get(h, "/mcp/hermes-dave/", map[string]string{"X-Dev-User": "dave@brickeye.com"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("pre-reload: want 403, got %d", rr.Code)
	}

	// Write a new ACL that grants dave access to hermes-dave.
	newYAML := testACLYAML + "\n  dave@brickeye.com:\n    banks: [hermes-dave]\n"
	f := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(f, []byte(newYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	newACL, err := authz.Load(f)
	if err != nil {
		t.Fatalf("Load new ACL: %v", err)
	}

	h.SetACL(newACL)

	// dave → hermes-dave: now allowed.
	rr = get(h, "/mcp/hermes-dave/", map[string]string{"X-Dev-User": "dave@brickeye.com"})
	if rr.Code != http.StatusOK {
		t.Fatalf("post-reload: want 200, got %d", rr.Code)
	}

	// Existing grants still work after reload.
	rr = get(h, "/mcp/hermes-alice/", map[string]string{"X-Dev-User": "alice@brickeye.com"})
	if rr.Code != http.StatusOK {
		t.Fatalf("post-reload alice still works: want 200, got %d", rr.Code)
	}
}

// AC: HTTP API surface (/v1/<tenant>/banks/<bankID>/...) is gated the same as MCP.
func TestHTTPAPIPath_Allowed_200(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := post(h, "/v1/default/banks/hermes-alice/retain",
		map[string]string{"X-Dev-User": "alice@brickeye.com"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestHTTPAPIPath_Denied_403(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := post(h, "/v1/default/banks/hermes-bob/retain",
		map[string]string{"X-Dev-User": "alice@brickeye.com"})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
	spy.mu.Lock()
	calls := spy.callCount
	spy.mu.Unlock()
	if calls != 0 {
		t.Errorf("denied request must not reach upstream; got %d calls", calls)
	}
}

// AC: case-insensitive identity matching — uppercase email is treated same as lowercase.
func TestCaseInsensitiveIdentity(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-alice/", map[string]string{"X-Dev-User": "ALICE@BRICKEYE.COM"})

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (uppercase email should match ACL)", rr.Code)
	}
}

// AC: unknown email with no ACL entry cannot access any non-shared bank.
func TestUnknownEmail_NonSharedBank_403(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	rr := get(h, "/mcp/hermes-stranger/", map[string]string{"X-Dev-User": "stranger@other.com"})

	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

// AC: concurrent requests are safe (no data race on the ACL atomic pointer).
func TestConcurrentRequests_NoRace(t *testing.T) {
	spy := &upstreamSpy{}
	upstream := httptest.NewServer(spy.handler())
	defer upstream.Close()
	h := newHandler(t, upstream, loadACL(t))

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			// Mix of allowed, denied, and reload operations.
			if i%5 == 0 {
				h.SetACL(loadACL(t))
			} else if i%2 == 0 {
				get(h, "/mcp/hermes-alice/", map[string]string{"X-Dev-User": "alice@brickeye.com"})
			} else {
				get(h, "/mcp/hermes-bob/", map[string]string{"X-Dev-User": "alice@brickeye.com"})
			}
		}(i)
	}
	wg.Wait()
}
