// Package remote provides the shared verified OpenSSH path for every managed
// server, including the panel host. It does not establish remote operation state.
package remote

import (
	"bytes"
	"errors"
	"net/netip"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

var ErrTarget = errors.New("invalid or unverified SSH target")

var (
	tailscaleIPv4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	userName      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// Target can only be populated by matching a candidate host key against an
// independently confirmed fingerprint. Obtaining the candidate over the same
// untrusted connection does not independently confirm its fingerprint.
type Target struct {
	address       netip.Addr
	user          string
	hostKey       string
	hostAlgorithm string
}

func NewTarget(address, user string, authorizedHostKey []byte, confirmedFingerprint string) (Target, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" || (!tailscaleIPv4.Contains(ip) && !tailscaleIPv6.Contains(ip)) || !userName.MatchString(user) || len(authorizedHostKey) > 16<<10 {
		return Target{}, ErrTarget
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(authorizedHostKey)
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || ssh.FingerprintSHA256(key) != confirmedFingerprint {
		return Target{}, ErrTarget
	}

	algorithm := key.Type()
	switch algorithm {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
	case ssh.KeyAlgoRSA:
		// A plain RSA host key must use SHA-2 signatures, never legacy ssh-rsa.
		algorithm = ssh.KeyAlgoRSASHA512 + "," + ssh.KeyAlgoRSASHA256
	default:
		return Target{}, ErrTarget
	}
	return Target{address: ip, user: user, hostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), hostAlgorithm: algorithm}, nil
}

func (target Target) valid() bool {
	return target.address.IsValid() && target.user != "" && target.hostKey != "" && target.hostAlgorithm != ""
}
