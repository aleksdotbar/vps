package transport

import (
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/tsaddr"
	"tailscale.com/tailcfg"
)

var (
	ErrIdentity    = errors.New("private HTTPS origin or Tailscale identity is not ready")
	ErrCertificate = errors.New("private HTTPS certificate is unavailable")
	ErrBusy        = errors.New("private HTTPS certificate admission is busy")
)

type identity struct {
	hostname  string
	nodeID    tailcfg.StableNodeID
	addresses []netip.Addr
}

// NativeSelf derives setup's private origin from the same native identity policy
// used by the production listener. Callers cannot select another certificate name.
func NativeSelf(status *ipnstate.Status) (string, string, []netip.Addr, error) {
	if status == nil || status.Self == nil {
		return "", "", nil, ErrIdentity
	}
	origin := "https://" + strings.TrimSuffix(status.Self.DNSName, ".")
	selected, err := identityFor(origin, status)
	if err != nil {
		return "", "", nil, err
	}
	return origin, string(selected.nodeID), slices.Clone(selected.addresses), nil
}

// identityFor is pure: only this verified self identity can choose production
// listeners or certificate names. Neither DNS lookup nor request headers bind us.
func identityFor(origin string, status *ipnstate.Status) (identity, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" || parsed.Host != parsed.Hostname() || parsed.Host != strings.ToLower(parsed.Host) {
		return identity{}, ErrIdentity
	}
	hostname := parsed.Hostname()
	if origin != "https://"+hostname {
		return identity{}, ErrIdentity
	}
	if len(hostname) > 253 || !strings.Contains(hostname, ".") {
		return identity{}, ErrIdentity
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return identity{}, ErrIdentity
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return identity{}, ErrIdentity
			}
		}
	}
	if status == nil || status.BackendState != "Running" || !status.TUN || status.Self == nil || status.Self.ID == "" || status.CurrentTailnet == nil || !status.CurrentTailnet.MagicDNSEnabled {
		return identity{}, ErrIdentity
	}
	suffix := status.CurrentTailnet.MagicDNSSuffix
	if suffix == "" || !strings.HasSuffix(hostname, "."+suffix) || strings.TrimSuffix(status.Self.DNSName, ".") != hostname || !slices.Contains(status.CertDomains, hostname) {
		return identity{}, ErrIdentity
	}
	if len(status.TailscaleIPs) < 1 || len(status.TailscaleIPs) > 2 || len(status.Self.TailscaleIPs) != len(status.TailscaleIPs) {
		return identity{}, ErrIdentity
	}
	addresses := slices.Clone(status.TailscaleIPs)
	slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
	for index, address := range addresses {
		if !address.IsValid() || address.Zone() != "" || address.Is4In6() || !tsaddr.IsTailscaleIP(address) || !slices.Contains(status.Self.TailscaleIPs, address) || (index > 0 && addresses[index-1] == address) {
			return identity{}, ErrIdentity
		}
	}
	if len(addresses) == 2 && addresses[0].Is4() == addresses[1].Is4() {
		return identity{}, ErrIdentity
	}
	return identity{hostname: hostname, nodeID: status.Self.ID, addresses: addresses}, nil
}
func (i identity) same(other identity) bool {
	return i.hostname == other.hostname && i.nodeID == other.nodeID && slices.Equal(i.addresses, other.addresses)
}
