package hostsetup

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/aleksdotbar/vps/panel/internal/foundation"
)

var ErrRuntimeSSH = errors.New("runtime SSH authority cannot be preserved by the supported setup")

const (
	maxSSHFileBytes      = 16 << 10
	maxSSHAuthorityBytes = 64 << 10
	maxSSHRecordBytes    = 96 << 10
)

type sshAuthorityFile struct {
	User    string `json:"user"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Active  bool   `json:"active"`
	Present bool   `json:"present"`
	UID     uint32 `json:"uid"`
	Mode    uint32 `json:"mode"`
	Bytes   []byte `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Keys    int    `json:"keys"`
}

type sshObservation struct {
	ConfigSHA256    string             `json:"config_sha256"`
	EffectiveSHA256 string             `json:"effective_sha256"`
	Files           []sshAuthorityFile `json:"files"`
}

type sshEffective map[string][]string

func (settings sshEffective) scalar(name string) string {
	values := settings[name]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func parseSSHEffective(data []byte) (sshEffective, error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, ErrRuntimeSSH
	}
	result := make(sshEffective)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name == "" || len(value) > 16<<10 {
			return nil, ErrRuntimeSSH
		}
		result[strings.ToLower(name)] = append(result[strings.ToLower(name)], strings.TrimSpace(value))
	}
	return result, nil
}

func confirmSSHPolicy(settings sshEffective, input foundation.Input, sourceFiles []string) error {
	root := settings.scalar("permitrootlogin")
	if root == "without-password" {
		root = "prohibit-password"
	}
	yesNo := func(value bool) string {
		if value {
			return "yes"
		}
		return "no"
	}
	if settings.scalar("port") != "22" || root != input.Bootstrap.RootLogin || settings.scalar("passwordauthentication") != yesNo(input.Bootstrap.PasswordAuthentication) ||
		settings.scalar("kbdinteractiveauthentication") != yesNo(input.Bootstrap.KeyboardInteractiveAuthentication) || settings.scalar("pubkeyauthentication") != "yes" ||
		settings.scalar("usepam") != "yes" || settings.scalar("strictmodes") != "yes" || settings.scalar("permituserenvironment") != "no" {
		return ErrRuntimeSSH
	}
	methods := settings.scalar("authenticationmethods")
	if methods != "any" && methods != "publickey" {
		return ErrRuntimeSSH
	}
	if methods == "publickey" && (input.Bootstrap.PasswordAuthentication || input.Bootstrap.KeyboardInteractiveAuthentication) {
		return ErrRuntimeSSH
	}
	if settings.scalar("hostbasedauthentication") != "no" {
		return ErrRuntimeSSH
	}
	for _, name := range []string{"gssapiauthentication", "kerberosauthentication"} {
		values := settings[name]
		if len(values) > 1 || (len(values) == 1 && values[0] != "no") {
			return ErrRuntimeSSH
		}
	}
	for _, name := range []string{"authorizedkeyscommand", "authorizedprincipalscommand", "authorizedprincipalsfile", "trustedusercakeys", "forcecommand", "chrootdirectory"} {
		if settings.scalar(name) != "none" {
			return ErrRuntimeSSH
		}
	}
	for _, name := range []string{"allowgroups", "denygroups", "denyusers"} {
		if len(settings[name]) != 0 {
			return ErrRuntimeSSH
		}
	}
	for _, value := range settings["allowusers"] {
		for _, user := range strings.Fields(value) {
			if user != "root" && user != input.Operator.Name && user != "panel-runner" {
				return ErrRuntimeSSH
			}
		}
	}
	actual := []string{}
	for _, value := range settings["authorizedkeysfile"] {
		actual = append(actual, strings.Fields(value)...)
	}
	if len(actual) == 0 || !slices.Equal(actual, sourceFiles) {
		return ErrRuntimeSSH
	}
	for _, source := range actual {
		switch source {
		case "%h/.ssh/authorized_keys", "/etc/ssh/authorized_keys.d/%u", "/var/lib/panel-access/%u/authorized_keys":
		default:
			return ErrRuntimeSSH
		}
	}
	if !slices.Contains(strings.Split(settings.scalar("pubkeyacceptedalgorithms"), ","), ssh.KeyAlgoED25519) {
		return ErrRuntimeSSH
	}
	return nil
}

func confirmSSHLoginUsers(settings sshEffective, operator string, accounts accountSnapshot) error {
	if len(settings["allowusers"]) != 0 {
		return nil
	}
	// Without AllowUsers, every existing login shell is another human authority
	// whose public keys this bounded root/operator preservation path cannot retain.
	for name, account := range accounts.users {
		if name != "root" && name != operator && !defaultNoLoginShell.MatchString(resolveAccountShell(account.shell)) {
			return ErrRuntimeSSH
		}
	}
	return nil
}

func admitSSHAuthority(input foundation.Input, settings sshEffective, files []sshAuthorityFile) ([]sshAuthorityFile, error) {
	selected, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(input.Operator.PublicKey))
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrRuntimeSSH
	}
	files = append([]sshAuthorityFile(nil), files...)
	total, keys := 0, 0
	for index := range files {
		file := &files[index]
		file.Keys = 0
		if !file.Present && len(file.Bytes) != 0 {
			return nil, ErrRuntimeSSH
		}
		total += len(file.Bytes)
		if len(file.Bytes) > maxSSHFileBytes || total > maxSSHAuthorityBytes {
			return nil, ErrRuntimeSSH
		}
		for _, line := range bytes.Split(file.Bytes, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			key, _, options, rest, err := ssh.ParseAuthorizedKey(line)
			if err != nil || len(bytes.TrimSpace(rest)) != 0 || !supportedAuthorityKey(key, settings) || !file.Active {
				return nil, ErrRuntimeSSH
			}
			for _, option := range options {
				if !supportedAuthorityOption(option) {
					return nil, ErrRuntimeSSH
				}
			}
			if len(options) > 0 && bytes.Equal(key.Marshal(), selected.Marshal()) {
				return nil, ErrRuntimeSSH
			}
			file.Keys++
			keys++
			if keys > 128 {
				return nil, ErrRuntimeSSH
			}
		}
		if file.Present {
			digest := sha256.Sum256(file.Bytes)
			file.SHA256 = hex.EncodeToString(digest[:])
		}
	}
	return files, nil
}

func supportedAuthorityOption(option string) bool {
	name, value, hasValue := strings.Cut(option, "=")
	switch strings.ToLower(name) {
	case "restrict", "no-agent-forwarding", "no-port-forwarding", "no-pty", "no-user-rc", "no-x11-forwarding", "verify-required", "no-touch-required":
		return !hasValue
	case "from", "command", "permitopen", "permitlisten", "expiry-time":
		return hasValue && value != ""
	default:
		return false
	}
}

func supportedAuthorityKey(key ssh.PublicKey, settings sshEffective) bool {
	if _, certificate := key.(*ssh.Certificate); certificate {
		return false
	}
	algorithms := strings.Split(settings.scalar("pubkeyacceptedalgorithms"), ",")
	if key.Type() == ssh.KeyAlgoRSA {
		minimum, err := strconv.ParseInt(settings.scalar("requiredrsasize"), 10, 32)
		public, ok := key.(ssh.CryptoPublicKey)
		if err != nil || minimum <= 0 || !ok {
			return false
		}
		rsaKey, ok := public.CryptoPublicKey().(*rsa.PublicKey)
		return ok && rsaKey != nil && rsaKey.N != nil && int64(rsaKey.N.BitLen()) >= minimum &&
			(slices.Contains(algorithms, ssh.KeyAlgoRSASHA256) || slices.Contains(algorithms, ssh.KeyAlgoRSASHA512))
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return slices.Contains(algorithms, key.Type())
	default:
		return false
	}
}

// This is only a safety gate for external reads and unsupported per-human Match
// authority. Native sshd remains the sole configuration parser/evaluator.
func admittedSSHConfig(data []byte) bool {
	if len(data) == 0 || len(data) > 64<<10 {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.FieldsFunc(line, func(value rune) bool { return value == ' ' || value == '\t' || value == '=' })
		if len(fields) == 0 {
			return false
		}
		switch strings.ToLower(fields[0]) {
		case "include":
			return false
		case "match":
			if len(fields) == 2 && strings.EqualFold(fields[1], "all") {
				continue
			}
			if len(fields) != 3 || !strings.EqualFold(fields[1], "user") || fields[2] != "panel-runner" {
				return false
			}
		}
	}
	return true
}
