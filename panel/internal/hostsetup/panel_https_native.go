package hostsetup

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"tailscale.com/client/local"

	"github.com/aleksdotbar/vps/panel/internal/transport"
)

type panelSelf struct {
	origin, nodeID string
	addresses      []netip.Addr
}

func (selected panelSelf) same(other panelSelf) bool {
	return selected.origin == other.origin && selected.nodeID == other.nodeID && slices.Equal(selected.addresses, other.addresses)
}
func panelNativeSelf(ctx context.Context) (panelSelf, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := local.Client{UseSocketOnly: true}
	status, err := client.StatusWithoutPeers(bounded)
	if err != nil {
		return panelSelf{}, ErrPanelHTTPS
	}
	origin, nodeID, addresses, err := transport.NativeSelf(status)
	if err != nil {
		return panelSelf{}, ErrPanelHTTPS
	}
	return panelSelf{origin, nodeID, addresses}, nil
}

func panelPrivateProof(ctx context.Context, system, operation string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	process := nativeCommand(bounded, system+"/sw/bin/runuser", "--user", "panel", "--group", "panel", "--", system+"/sw/bin/env", "-i", "HOME=/var/lib/panel", "USER=panel", "LOGNAME=panel", "PATH="+system+"/sw/bin:/run/wrappers/bin", system+"/sw/bin/panel", "install", "panel-proof", "--operation-id", operation)
	output := inspectionOutput{cancel: cancel}
	process.Stdout = &output
	if process.Run() != nil || output.buffer.Len() > 8<<10 {
		return nil, ErrPanelHTTPS
	}
	return output.buffer.Bytes(), nil
}

type panelUnit struct{ active, sub, invocation, pid string }

func panelService(ctx context.Context, system string) (panelUnit, error) {
	data, err := runtimeProbe(ctx, system+"/sw/bin/systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,FragmentPath,DropInPaths,NeedDaemonReload,User,Group,MainPID,InvocationID", "--", "panel.service")
	if err != nil || len(data) > 16<<10 {
		return panelUnit{}, ErrPanelHTTPS
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return panelUnit{}, ErrPanelHTTPS
		}
		if _, exists := fields[key]; exists {
			return panelUnit{}, ErrPanelHTTPS
		}
		fields[key] = value
	}
	if len(fields) != 10 || fields["LoadState"] != "loaded" || fields["NeedDaemonReload"] != "no" || fields["User"] != "panel" || fields["Group"] != "panel" || !sameNativeFile(fields["FragmentPath"], system+"/etc/systemd/system/panel.service") || !nativeUnitDropins(strings.Fields(fields["DropInPaths"]), system+"/etc/systemd/system/panel.service.d") {
		return panelUnit{}, ErrPanelHTTPS
	}
	return panelUnit{fields["ActiveState"], fields["SubState"], fields["InvocationID"], fields["MainPID"]}, nil
}
func panelProcess(system string, unit panelUnit) bool {
	pid, err := strconv.ParseUint(unit.pid, 10, 31)
	return err == nil && pid > 0 && unit.active == "active" && unit.sub == "running" && len(unit.invocation) == 32 && sameNativeFile("/proc/"+strconv.FormatUint(pid, 10)+"/exe", system+"/sw/bin/panel")
}

func panelHealth(ctx context.Context, selected panelSelf) error {
	hostname := strings.TrimPrefix(selected.origin, "https://")
	// Dial only the verified numeric native Self addresses. Certificate/SNI and
	// system roots remain mandatory; neither proxies, DNS nor redirects choose a peer.
	clientTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8 << 10, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != net.JoinHostPort(hostname, "443") {
			return nil, ErrPanelHTTPS
		}
		for _, ip := range selected.addresses {
			connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
			if err == nil {
				return connection, nil
			}
		}
		return nil, ErrPanelHTTPS
	}}
	defer clientTransport.CloseIdleConnections()
	client := &http.Client{Transport: clientTransport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrPanelHTTPS }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, selected.origin+"/healthz", nil)
	if err != nil {
		return ErrPanelHTTPS
	}
	response, err := client.Do(request)
	if err != nil {
		return ErrPanelHTTPS
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 33))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		return ErrPanelHTTPS
	}
	return nil
}
func awaitPanelHTTPS(parent context.Context, system string, selected panelSelf) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	for {
		before, err := panelService(ctx, system)
		if err != nil {
			return ErrPanelHTTPS
		}
		if panelProcess(system, before) && panelHealth(ctx, selected) == nil {
			after, err := panelService(ctx, system)
			if err == nil && before == after && panelProcess(system, after) {
				return nil
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ErrPanelHTTPS
		case <-timer.C:
		}
	}
}

func panelConfigAbsent() bool {
	_, err := os.Lstat("/var/lib/panel-config")
	return os.IsNotExist(err)
}
