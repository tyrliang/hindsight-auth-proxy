// Package authz implements bank-level access control for hindsight-auth-proxy.
//
// The ACL is a YAML file with four top-level keys:
//   - admins:  identities that bypass all bank checks (full access incl. unscoped paths).
//   - shared:  bank glob patterns accessible to every authenticated tailnet caller.
//   - teams:   per-team bank patterns + member lists.
//   - users:   per-identity bank patterns, additive on top of shared+team grants.
//
// An "identity" is normally a human's Tailscale login email (e.g.
// "alice@brickeye.com"). Tagged tailnet nodes — service/agent processes with
// no personal login, such as an automated Hermes assistant — are identified
// by their short tailnet hostname instead (e.g. "agent-product-assistant");
// the same admins/shared/teams/users keys and glob matching apply to both,
// since matching is purely string-based. See internal/identity.Resolve for
// how the caller derives which one applies.
//
// Bank ids never contain "/", so path.Match "*" matches any bank name and
// "team-sw-*" matches any bank starting with "team-sw-".
package authz

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ACL is the parsed and validated access-control list.
type ACL struct {
	Admins []string        `yaml:"admins"`
	Shared []string        `yaml:"shared"`
	Teams  map[string]team `yaml:"teams"`
	Users  map[string]user `yaml:"users"`
}

type team struct {
	Banks   []string `yaml:"banks"`
	Members []string `yaml:"members"`
}

type user struct {
	Banks []string `yaml:"banks"`
}

// Load reads, parses, and validates the YAML ACL file at p.
// Every bank pattern is validated with path.Match at load time so runtime
// matching can never panic on a malformed pattern.
func Load(p string) (*ACL, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading ACL file %q: %w", p, err)
	}

	var a ACL
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parsing ACL YAML: %w", err)
	}

	// Validate all patterns by doing a trial path.Match with a dummy string.
	allPatterns := append([]string(nil), a.Shared...)
	for _, t := range a.Teams {
		allPatterns = append(allPatterns, t.Banks...)
	}
	for _, u := range a.Users {
		allPatterns = append(allPatterns, u.Banks...)
	}
	for _, pat := range allPatterns {
		if _, err := path.Match(pat, "dummy"); err != nil {
			return nil, fmt.Errorf("invalid bank pattern %q: %w", pat, err)
		}
	}

	return &a, nil
}

// IsAdmin reports whether identity is an admin. Admins bypass all bank ACL
// checks and may access unscoped paths (enumeration, metrics, docs, etc.).
func (a *ACL) IsAdmin(identity string) bool {
	identity = strings.ToLower(identity)
	for _, adm := range a.Admins {
		if strings.ToLower(adm) == identity {
			return true
		}
	}
	return false
}

// Allowed reports whether identity is permitted to access bankID. identity is
// an email for human callers or a tailnet hostname for tagged agent nodes
// (see the package doc); matching is identical for both.
//
// Grant order:
//  1. Admins → always true.
//  2. Shared patterns → true for every authenticated caller.
//  3. Team banks → true if identity is a member of any team whose bank pattern matches.
//  4. Identity-specific banks → true if identity has an explicit pattern that matches.
//
// Default deny: no matching pattern → false.
func (a *ACL) Allowed(identity, bankID string) bool {
	identity = strings.ToLower(identity)

	if a.IsAdmin(identity) {
		return true
	}

	for _, pat := range a.Shared {
		if matchPattern(pat, bankID) {
			return true
		}
	}

	for _, t := range a.Teams {
		if !memberOf(t.Members, identity) {
			continue
		}
		for _, pat := range t.Banks {
			if matchPattern(pat, bankID) {
				return true
			}
		}
	}

	if u, ok := a.Users[identity]; ok {
		for _, pat := range u.Banks {
			if matchPattern(pat, bankID) {
				return true
			}
		}
	}

	return false
}

// matchPattern wraps path.Match; panics are impossible because patterns are
// validated at Load time. Returns false on error (should never happen).
func matchPattern(pat, name string) bool {
	ok, _ := path.Match(pat, name)
	return ok
}

func memberOf(members []string, identity string) bool {
	for _, m := range members {
		if strings.ToLower(m) == identity {
			return true
		}
	}
	return false
}

// mcpRE matches /mcp/<bankID> or /mcp/<bankID>/...
// Bank ids contain no "/" so [^/]+ matches exactly one component.
var mcpRE = regexp.MustCompile(`^/mcp/([^/]+)(?:/|$)`)

// apiRE matches /v1/<tenant>/banks/<bankID> or /v1/<tenant>/banks/<bankID>/...
var apiRE = regexp.MustCompile(`^/v1/[^/]+/banks/([^/]+)(?:/|$)`)

// BankFromPath extracts the bank id from a Hindsight URL path.
//
// Scoped paths (those that target a specific bank) return (bankID, true).
// Unscoped paths — root, /metrics, /docs, /v1/<tenant>/banks (list), etc. —
// return ("", false). The caller decides whether to allow unscoped access
// (admins only) or block it.
func BankFromPath(p string) (bankID string, scoped bool) {
	if m := mcpRE.FindStringSubmatch(p); m != nil {
		return m[1], true
	}
	if m := apiRE.FindStringSubmatch(p); m != nil {
		return m[1], true
	}
	return "", false
}
