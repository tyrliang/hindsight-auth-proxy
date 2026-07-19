// Package identity derives the ACL identity string for a tailnet caller from
// a tsnet WhoIs response.
package identity

import (
	"strings"

	"tailscale.com/client/tailscale/apitype"
)

// Resolve derives the ACL identity for a caller from a tsnet WhoIs response.
//
// Two kinds of tailnet peers connect to this proxy:
//
//   - Human tailnet members authenticate as themselves. Identity is their
//     Tailscale login email (UserProfile.LoginName), e.g. "alice@brickeye.com".
//
//   - Tagged nodes — service/agent processes joined via a tag-owned auth key
//     (e.g. an automated Hermes assistant) — have no personal login.
//     tailcfg.Node.User only reflects whoever's auth key created the node,
//     not the ACL identity it runs as, so it must never be trusted for these.
//     Identity instead falls back to the node's short tailnet hostname
//     (e.g. "agent-product-assistant"), letting acl.yaml grant access by
//     machine name through the same `users:` map used for email grants.
//
// Returns "" when identity cannot be determined (no Node, or an untagged
// node with no UserProfile) — the caller treats "" as unauthenticated.
func Resolve(resp *apitype.WhoIsResponse) string {
	if resp == nil || resp.Node == nil {
		return ""
	}
	if len(resp.Node.Tags) > 0 {
		return machineHostname(resp.Node.Name)
	}
	if resp.UserProfile == nil {
		return ""
	}
	return resp.UserProfile.LoginName
}

// machineHostname extracts the short hostname from a tailnet FQDN, e.g.
// "agent-product-assistant.tailnet-name.ts.net." -> "agent-product-assistant".
func machineHostname(fqdn string) string {
	name := strings.TrimSuffix(fqdn, ".")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return name
}
