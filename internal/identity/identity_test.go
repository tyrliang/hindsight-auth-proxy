package identity

import (
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		resp *apitype.WhoIsResponse
		want string
	}{
		{
			name: "nil response",
			resp: nil,
			want: "",
		},
		{
			name: "nil node",
			resp: &apitype.WhoIsResponse{},
			want: "",
		},
		{
			name: "human user, no tags",
			resp: &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{Name: "alices-laptop.tail-scale.ts.net."},
				UserProfile: &tailcfg.UserProfile{LoginName: "alice@brickeye.com"},
			},
			want: "alice@brickeye.com",
		},
		{
			name: "untagged node, no user profile (unauthenticated)",
			resp: &apitype.WhoIsResponse{
				Node: &tailcfg.Node{Name: "mystery.tail-scale.ts.net."},
			},
			want: "",
		},
		{
			name: "tagged agent node falls back to hostname, even with a stale UserProfile",
			resp: &apitype.WhoIsResponse{
				Node: &tailcfg.Node{
					Name: "agent-product-assistant.tail-scale.ts.net.",
					Tags: []string{"tag:agent"},
				},
				// Node.User (and thus UserProfile) reflects whoever's auth key
				// created the node, not the ACL identity it runs as — must be
				// ignored once the node is tagged.
				UserProfile: &tailcfg.UserProfile{LoginName: "richard@brickeye.com"},
			},
			want: "agent-product-assistant",
		},
		{
			name: "tagged agent node, single-label hostname (no MagicDNS suffix)",
			resp: &apitype.WhoIsResponse{
				Node: &tailcfg.Node{
					Name: "agent-support-assistant",
					Tags: []string{"tag:agent"},
				},
			},
			want: "agent-support-assistant",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.resp)
			if got != tc.want {
				t.Errorf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMachineHostname(t *testing.T) {
	cases := []struct {
		fqdn string
		want string
	}{
		{"agent-product-assistant.tail1234.ts.net.", "agent-product-assistant"},
		{"agent-support-assistant.tail1234.ts.net", "agent-support-assistant"},
		{"bare-hostname", "bare-hostname"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := machineHostname(tc.fqdn); got != tc.want {
			t.Errorf("machineHostname(%q) = %q, want %q", tc.fqdn, got, tc.want)
		}
	}
}
