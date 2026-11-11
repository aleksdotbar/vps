package main

import (
	"bytes"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/hostsetup"
)

// installStep is what one `panel install` invocation does next. Steps that
// need a fresh SSH login stop the run and tell the operator where to log in.
type installStep int

const (
	stepEnroll installStep = iota
	stepWaitEnrollment
	stepConfirmEnrollment
	stepPanel
	stepFinal
	stepWaitFinal
	stepConfirmFinal
	stepDone
	stepFailed
)

func nextStep(status hostsetup.SetupStatus) installStep {
	if status.Final != nil {
		switch status.Final.Status {
		case "final_active":
			return stepDone
		case "awaiting_private":
			return stepConfirmFinal
		case "starting", "activating":
			return stepWaitFinal
		}
		return stepFailed
	}
	if status.Enrollment == nil {
		return stepEnroll
	}
	switch status.Enrollment.Status {
	case "starting", "enrolling":
		return stepWaitEnrollment
	case "awaiting_bootstrap":
		return stepConfirmEnrollment
	case "enrolled":
	default:
		return stepFailed
	}
	if status.HTTPS != nil && status.HTTPS.Status == "retained" {
		return stepFinal
	}
	return stepPanel
}

// loginSession describes the SSH login that started this process.
type loginSession struct {
	Started       time.Time
	SSHConnection string
}

// Linux reports process start times in USER_HZ ticks, fixed at 100.
const userHZ = 100

// currentLoginSession walks the process ancestry to the nearest sshd session
// process. Console and other non-SSH sessions cannot prove SSH access.
func currentLoginSession() (loginSession, error) {
	boot, err := bootTime()
	if err != nil {
		return loginSession{}, err
	}
	var session loginSession
	for pid, depth := os.Getpid(), 0; pid > 1 && depth < 64; depth++ {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return loginSession{}, errors.New("process ancestry unavailable")
		}
		stat, err := parseProcStat(data)
		if err != nil {
			return loginSession{}, err
		}
		if session.SSHConnection == "" {
			session.SSHConnection = processEnvironment(pid, "SSH_CONNECTION")
		}
		if stat.comm == "sshd" || stat.comm == "sshd-session" {
			session.Started = boot.Add(time.Duration(stat.startTicks) * time.Second / userHZ)
			return session, nil
		}
		pid = stat.ppid
	}
	return loginSession{}, errors.New("run this from an SSH session; a console session does not prove SSH access")
}

type procStat struct {
	comm       string
	ppid       int
	startTicks uint64
}

// parseProcStat reads comm, ppid and starttime from /proc/<pid>/stat. The
// command name may contain spaces and parentheses, so fields follow the last ')'.
func parseProcStat(data []byte) (procStat, error) {
	open, closing := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return procStat{}, errors.New("unsupported process status")
	}
	fields := strings.Fields(string(data[closing+1:]))
	// After comm: state(0) ppid(1) ... starttime(19).
	if len(fields) < 20 {
		return procStat{}, errors.New("unsupported process status")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procStat{}, errors.New("unsupported process status")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return procStat{}, errors.New("unsupported process status")
	}
	return procStat{comm: string(data[open+1 : closing]), ppid: ppid, startTicks: start}, nil
}

func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, errors.New("boot time unavailable")
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil {
				return time.Unix(seconds, 0), nil
			}
		}
	}
	return time.Time{}, errors.New("boot time unavailable")
}

func processEnvironment(pid int, name string) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return ""
	}
	for entry := range bytes.SplitSeq(data, []byte{0}) {
		if value, ok := bytes.CutPrefix(entry, []byte(name+"=")); ok {
			return string(value)
		}
	}
	return ""
}

// startedAfter is true when the login began after the recorded phase change,
// so it authenticated against the newly activated configuration.
func (session loginSession) startedAfter(phase time.Time) bool {
	return session.Started.After(phase)
}

// overTailnet is true when another tailnet device connected to this host's
// Tailscale address. A client using the host's own address came through the host
// itself (a jump or console session), which proves no private remote access.
func (session loginSession) overTailnet() bool {
	fields := strings.Fields(session.SSHConnection)
	if len(fields) != 4 {
		return false
	}
	client, clientErr := netip.ParseAddr(fields[0])
	server, serverErr := netip.ParseAddr(fields[2])
	if clientErr != nil || serverErr != nil {
		return false
	}
	client, server = client.Unmap(), server.Unmap()
	return tailnetAddress(client) && tailnetAddress(server) && client != server
}

var (
	tailnetIPv4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetIPv6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func tailnetAddress(address netip.Addr) bool {
	return tailnetIPv4.Contains(address) || tailnetIPv6.Contains(address)
}
