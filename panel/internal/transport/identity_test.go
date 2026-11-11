package transport

import (
	"net/netip"
	"tailscale.com/ipn/ipnstate"
	"testing"
)

const fixtureName = "panel.generic.ts.net"

func nativeStatus() *ipnstate.Status {
	addresses := []netip.Addr{netip.MustParseAddr("100.80.1.2"), netip.MustParseAddr("fd7a:115c:a1e0::12")}
	return &ipnstate.Status{BackendState: "Running", TUN: true, TailscaleIPs: addresses, Self: &ipnstate.PeerStatus{ID: "nGeneric", DNSName: fixtureName + ".", TailscaleIPs: addresses}, CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSEnabled: true, MagicDNSSuffix: "generic.ts.net"}, CertDomains: []string{fixtureName}}
}
func TestPrivateIdentityRejectsUnreadyAliasPublicAndNoncanonicalBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ipnstate.Status)
	}{
		{"stopped", func(status *ipnstate.Status) { status.BackendState = "Stopped" }},
		{"userspace", func(status *ipnstate.Status) { status.TUN = false }},
		{"missing self", func(status *ipnstate.Status) { status.Self = nil }},
		{"missing stable identity", func(status *ipnstate.Status) { status.Self.ID = "" }},
		{"different self name", func(status *ipnstate.Status) { status.Self.DNSName = "other.generic.ts.net." }},
		{"certificate alias only", func(status *ipnstate.Status) { status.CertDomains = []string{"alias.generic.ts.net"} }},
		{"missing MagicDNS", func(status *ipnstate.Status) { status.CurrentTailnet.MagicDNSEnabled = false }},
		{"wrong tailnet", func(status *ipnstate.Status) { status.CurrentTailnet.MagicDNSSuffix = "other.ts.net" }},
		{"public address", func(status *ipnstate.Status) {
			status.TailscaleIPs = []netip.Addr{netip.MustParseAddr("203.0.113.1")}
			status.Self.TailscaleIPs = status.TailscaleIPs
		}},
		{"nonself address", func(status *ipnstate.Status) {
			status.Self.TailscaleIPs = []netip.Addr{netip.MustParseAddr("100.80.1.3"), status.TailscaleIPs[1]}
		}},
		{"duplicate address", func(status *ipnstate.Status) {
			status.TailscaleIPs = []netip.Addr{status.TailscaleIPs[0], status.TailscaleIPs[0]}
			status.Self.TailscaleIPs = status.TailscaleIPs
		}},
	} {
		status := nativeStatus()
		test.change(status)
		if _, err := identityFor("https://"+fixtureName, status); err == nil {
			t.Fatalf("unsafe %s identity accepted", test.name)
		}
	}
	for _, origin := range []string{"http://" + fixtureName, "https://" + fixtureName + ":443", "https://" + fixtureName + "?", "https://" + fixtureName + "#", "https://" + fixtureName + "/", "https://" + fixtureName + ".", "https://PANEL.generic.ts.net", "https://user@" + fixtureName, "https://alias.generic.ts.net"} {
		if _, err := identityFor(origin, nativeStatus()); err == nil {
			t.Fatalf("noncanonical origin accepted: %s", origin)
		}
	}
	selected, err := identityFor("https://"+fixtureName, nativeStatus())
	if err != nil || len(selected.addresses) != 2 {
		t.Fatalf("valid private identity rejected: %v", err)
	}
}
