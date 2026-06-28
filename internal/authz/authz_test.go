package authz_test

import (
	"os"
	"path/filepath"
	"testing"

	"main/internal/authz"
)

// testACLYAML is the reference ACL used across test cases, matching the
// shape of acl.yaml.example.
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

func loadTestACL(t *testing.T) *authz.ACL {
	t.Helper()
	f := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(f, []byte(testACLYAML), 0o600); err != nil {
		t.Fatalf("write temp ACL: %v", err)
	}
	a, err := authz.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return a
}

func TestBankFromPath(t *testing.T) {
	tests := []struct {
		path       string
		wantBank   string
		wantScoped bool
	}{
		// MCP surface
		{"/mcp/hermes-alice/", "hermes-alice", true},
		{"/mcp/hermes-alice", "hermes-alice", true},
		{"/mcp/hermes-alice/sse", "hermes-alice", true},
		{"/mcp/team-sw-roadmap/messages", "team-sw-roadmap", true},
		// MCP unscoped
		{"/mcp/", "", false},
		{"/mcp", "", false},
		// HTTP API surface
		{"/v1/default/banks/hermes-alice", "hermes-alice", true},
		{"/v1/default/banks/hermes-alice/", "hermes-alice", true},
		{"/v1/default/banks/hermes-alice/recall", "hermes-alice", true},
		{"/v1/myorg/banks/team-sw-roadmap/retain", "team-sw-roadmap", true},
		// HTTP API unscoped (no bank id)
		{"/v1/default/banks", "", false},
		{"/v1/default/banks/", "", false},
		// Other paths
		{"/healthz", "", false},
		{"/metrics", "", false},
		{"/docs", "", false},
		{"/", "", false},
		{"", "", false},
	}

	for _, tc := range tests {
		got, scoped := authz.BankFromPath(tc.path)
		if got != tc.wantBank || scoped != tc.wantScoped {
			t.Errorf("BankFromPath(%q) = (%q, %v), want (%q, %v)",
				tc.path, got, scoped, tc.wantBank, tc.wantScoped)
		}
	}
}

func TestAllowed(t *testing.T) {
	a := loadTestACL(t)

	tests := []struct {
		email  string
		bank   string
		want   bool
		reason string
	}{
		// User-specific grants
		{"alice@brickeye.com", "hermes-alice", true, "user exact match"},
		{"alice@brickeye.com", "scratch-alice-notes", true, "user glob match"},
		{"alice@brickeye.com", "hermes-bob", false, "different user's bank"},
		{"alice@brickeye.com", "hermes-carol", false, "different user's bank"},
		// Team grants
		{"alice@brickeye.com", "team-sw-roadmap", true, "team-sw member"},
		{"bob@brickeye.com", "team-sw-roadmap", true, "team-sw member"},
		{"carol@brickeye.com", "team-rnd-experiments", true, "team-rnd member"},
		{"carol@brickeye.com", "team-sw-roadmap", false, "carol not in sw"},
		{"bob@brickeye.com", "team-rnd-experiments", false, "bob not in rnd"},
		// Shared banks — accessible to any authenticated user
		{"alice@brickeye.com", "org-handbook", true, "shared org-* for team member"},
		{"carol@brickeye.com", "org-handbook", true, "shared org-* for team member"},
		{"dave@brickeye.com", "org-handbook", true, "shared org-* for any authed user"},
		// Admin — full access regardless of bank or path scope
		{"richard@brickeye.com", "hermes-alice", true, "admin"},
		{"richard@brickeye.com", "hermes-stranger", true, "admin: arbitrary bank"},
		{"richard@brickeye.com", "team-sw-roadmap", true, "admin"},
		// Unknown email — no ACL entry; non-shared bank → deny
		{"stranger@other.com", "hermes-stranger", false, "unknown email, non-shared bank"},
		{"stranger@other.com", "team-sw-roadmap", false, "unknown email, team bank"},
		// Case-insensitivity
		{"ALICE@BRICKEYE.COM", "hermes-alice", true, "uppercase email"},
		{"Alice@Brickeye.com", "team-sw-roadmap", true, "mixed-case email"},
	}

	for _, tc := range tests {
		got := a.Allowed(tc.email, tc.bank)
		if got != tc.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v (%s)",
				tc.email, tc.bank, got, tc.want, tc.reason)
		}
	}
}

func TestIsAdmin(t *testing.T) {
	a := loadTestACL(t)

	if !a.IsAdmin("richard@brickeye.com") {
		t.Error("richard should be admin")
	}
	if !a.IsAdmin("RICHARD@BRICKEYE.COM") {
		t.Error("richard (uppercase) should be admin")
	}
	if a.IsAdmin("alice@brickeye.com") {
		t.Error("alice should not be admin")
	}
	if a.IsAdmin("unknown@other.com") {
		t.Error("unknown should not be admin")
	}
}

func TestLoadInvalidPattern(t *testing.T) {
	const badYAML = `
admins: []
shared: ["[invalid"]
teams: {}
users: {}
`
	f := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(f, []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := authz.Load(f)
	if err == nil {
		t.Error("Load with invalid glob pattern should return error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := authz.Load("/nonexistent/path/acl.yaml")
	if err == nil {
		t.Error("Load of missing file should return error")
	}
}

func TestLoadBytes(t *testing.T) {
	t.Run("valid YAML returns expected ACL", func(t *testing.T) {
		a, err := authz.LoadBytes([]byte(testACLYAML))
		if err != nil {
			t.Fatalf("LoadBytes: %v", err)
		}
		if len(a.Admins) != 1 || a.Admins[0] != "richard@brickeye.com" {
			t.Errorf("unexpected admins: %v", a.Admins)
		}
		if !a.IsAdmin("richard@brickeye.com") {
			t.Error("richard should be admin")
		}
		if !a.Allowed("alice@brickeye.com", "hermes-alice") {
			t.Error("alice should be allowed hermes-alice")
		}
		if a.Allowed("carol@brickeye.com", "hermes-alice") {
			t.Error("carol should not be allowed hermes-alice")
		}
	})

	t.Run("malformed YAML returns error", func(t *testing.T) {
		_, err := authz.LoadBytes([]byte("admins: [unclosed"))
		if err == nil {
			t.Error("malformed YAML should return error")
		}
	})

	t.Run("invalid bank pattern returns error", func(t *testing.T) {
		const badYAML = `
admins: []
shared: ["[invalid"]
teams: {}
users: {}
`
		_, err := authz.LoadBytes([]byte(badYAML))
		if err == nil {
			t.Error("invalid glob pattern should return error")
		}
	})
}
