package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/privatefile"
)

// Tool/store substitutions are private to native isolated-store tests. Production
// uses only current-system tools and the explicit root-local store, never PATH.
type importTools struct {
	nixStore   string
	systemdRun string
	store      string
}

func targetImportTools() importTools {
	return importTools{nixStore: "/run/current-system/sw/bin/nix-store", systemdRun: "/run/current-system/sw/bin/systemd-run", store: "local"}
}

func importEnvironment() []string {
	return []string{"HOME=/var/empty", "PATH=/run/current-system/sw/bin", "LANG=C", "LC_ALL=C",
		"NIX_CONF_DIR=/var/empty", "NIX_USER_CONF_FILES=", "NIX_CONFIG="}
}

func nativeCommand(ctx context.Context, command string, arguments ...string) *exec.Cmd {
	// #nosec G204 -- Production tools use fixed current-system or verified enrollment paths; private substitutions serve only local fixtures.
	process := exec.CommandContext(ctx, command, arguments...)
	process.Env = importEnvironment()
	process.Dir = "/"
	process.Stderr = io.Discard
	process.WaitDelay = 2 * time.Second
	// Each fixed native process owns its group. Pdeathsig terminates it if
	// its creating OS thread dies; explicit cancellation kills the whole group.
	// Neither event is interpreted as success.
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	process.Cancel = func() error {
		if process.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return process
}

type importOutput struct{ data bytes.Buffer }

func (output *importOutput) Write(data []byte) (int, error) {
	if len(data) > (3<<20)-output.data.Len() {
		return 0, ErrUnresolved
	}
	return output.data.Write(data)
}

func nativeQuery(ctx context.Context, tools importTools, arguments ...string) ([]byte, error) {
	process := nativeCommand(ctx, tools.nixStore, append([]string{"--store", tools.store}, arguments...)...)
	var output importOutput
	process.Stdout = &output
	if process.Run() != nil {
		return nil, ErrUnresolved
	}
	return output.data.Bytes(), nil
}

// observedImport never realizes/substitutes/builds paths. It requires the complete
// requisite union of both systems, including the panel, to be valid and rooted.
func observedImport(ctx context.Context, tools importTools, build localBuild) bool {
	roots := []string{build.EnrollmentSystem, build.FinalSystem}
	paths, err := nativeQuery(ctx, tools, append([]string{"--query", "--requisites"}, roots...)...)
	if err != nil {
		return false
	}
	lines := strings.Fields(string(paths))
	if len(lines) < 3 || len(lines) > 8192 {
		return false
	}
	panelFound := false
	seen := make(map[string]bool, len(lines))
	for _, path := range lines {
		if !strings.HasPrefix(path, "/nix/store/") || filepath.Clean(path) != path || filepath.Dir(path) != "/nix/store" || len(path) > 255 || seen[path] {
			return false
		}
		seen[path] = true
		panelFound = panelFound || path == build.PanelStorePath
	}
	if !panelFound || !seen[roots[0]] || !seen[roots[1]] {
		return false
	}
	registered, err := nativeQuery(ctx, tools, append([]string{"--query", "--roots"}, roots...)...)
	if err != nil {
		return false
	}
	for _, name := range []string{"enrollment", "final"} {
		found := false
		for line := range strings.SplitSeq(string(registered), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == filepath.Join(rootsDirectory(tools, build), name) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if _, err := nativeQuery(ctx, tools, append([]string{"--check-validity"}, lines...)...); err != nil {
		return false
	}
	// Isolated test stores relocate physical contents, while all Nix identities
	// remain canonical /nix/store paths. Production always uses plain local.
	binary := build.PanelStorePath + "/bin/panel"
	if strings.HasPrefix(tools.store, "local?root=") {
		binary = strings.TrimPrefix(tools.store, "local?root=") + binary
	}
	directory, err := os.OpenRoot(filepath.Dir(binary))
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	file, err := privatefile.OpenNoFollow(directory, "panel", os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}
func availableMemoryKiB() int64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
			available, err := strconv.ParseInt(fields[1], 10, 64)
			if err == nil && available > 0 {
				return available
			}
			return 0
		}
	}
	return 0
}
