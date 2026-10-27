// Package preflight probes the host before install: docker + compose v2, the
// memory Docker can give containers and the AVX2 flag (they drive the
// Discovery defaults and image variant), disk space, WSL, free ports.
// All command execution goes through Runner so every path is unit-testable.
package preflight

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Runner executes a command and returns its trimmed combined output.
type Runner func(name string, args ...string) (string, error)

// DefaultRunner is the real exec-backed Runner.
func DefaultRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type Host struct {
	OS, Arch     string
	WSL          bool
	RAMGigabytes int
	HasAVX2      bool
}

type Result struct {
	DockerOK, ComposeOK bool
	DockerVersion       string
	ComposeVersion      string
	// Problem says why Docker can't run the stack, and how to fix it (nil when
	// it can).
	Problem error
	// DockerMemGB is the memory Docker can give containers: the host's on
	// Linux, the VM's on Docker Desktop (0 when unknown).
	DockerMemGB   int
	DockerDesktop bool
	Host          Host
	Notes         []string
}

// MemoryGB is the memory the stack can actually use.
func (r Result) MemoryGB() int {
	if r.DockerMemGB > 0 {
		return r.DockerMemGB
	}
	return r.Host.RAMGigabytes
}

// DiscoveryMemGB is what Discovery (AudioMuse) wants while it analyses.
const DiscoveryMemGB = 8

// DiscoveryMemoryNote says, when it's so, that the stack has less memory than
// Discovery wants ("" otherwise, or when unknown): the reason the wizard
// leaves it off, or a warning when it's asked for anyway.
func (r Result) DiscoveryMemoryNote() string {
	gb := r.MemoryGB()
	if gb == 0 || gb >= DiscoveryMemGB {
		return ""
	}
	if r.DockerDesktop {
		return fmt.Sprintf("Docker Desktop gives containers %d GB of memory and Discovery (AudioMuse) wants ~%d GB while it analyses — raise it under Docker Desktop → Settings → Resources",
			gb, DiscoveryMemGB)
	}
	return fmt.Sprintf("this host has %d GB of memory and Discovery (AudioMuse) wants ~%d GB while it analyses", gb, DiscoveryMemGB)
}

// minDiskGB is the free space below which a first pull may not fit.
const minDiskGB = 8

// Run probes the host. It never fails on missing tooling — absence is data
// the install command turns into friendly guidance (Problem).
func Run(run Runner) (Result, error) {
	r := Result{}
	r.Host = detectHost()

	v, err := run("docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		r.Problem = dockerProblem(v, err)
	} else {
		r.DockerOK, r.DockerVersion = true, v
		if c, err := run("docker", "compose", "version", "--short"); err == nil {
			r.ComposeOK, r.ComposeVersion = true, c
		} else {
			r.Problem = errors.New("Docker's compose plugin is missing (`docker compose` doesn't run; the old docker-compose v1 isn't enough) — " +
				"install it: https://docs.docker.com/compose/install/linux/ — then re-run")
		}
	}
	if r.DockerOK {
		r.Notes = append(r.Notes, dockerNotes(run, &r)...)
	}
	if r.Host.WSL {
		r.Notes = append(r.Notes, "WSL detected — keep the music library on the Linux filesystem (not /mnt/c) for scan performance")
	}
	return r, nil
}

// dockerProblem turns a failed `docker version` into what to do about it.
func dockerProblem(out string, err error) error {
	low := strings.ToLower(out)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return errors.New("Docker isn't installed — install Docker Engine (Linux: https://docs.docker.com/engine/install/) " +
			"or Docker Desktop (macOS), then re-run")
	case strings.Contains(low, "permission denied"):
		return errors.New("Docker is running, but this user can't use it — add yourself to the docker group " +
			"(sudo usermod -aG docker $USER, then log out and back in) or run the installer with sudo")
	case strings.Contains(low, "cannot connect to the docker daemon"), strings.Contains(low, "daemon is running"),
		strings.Contains(low, "is the docker daemon running"), strings.Contains(low, "failed to connect to the docker api"),
		strings.Contains(low, "error during connect"):
		return errors.New("Docker is installed but not running — start it (Linux: sudo systemctl start docker; " +
			"macOS: open Docker Desktop) and re-run")
	}
	first, _, _ := strings.Cut(out, "\n")
	if first == "" {
		first = err.Error()
	}
	return fmt.Errorf("Docker isn't working: %s", first)
}

// dockerNotes reads what Docker can give the stack — memory, disk where its
// images live, its version — and warns about what will hurt.
func dockerNotes(run Runner, r *Result) []string {
	var notes []string
	if major, minor, ok := majorMinor(r.DockerVersion); ok && (major < 20 || (major == 20 && minor < 10)) {
		notes = append(notes, fmt.Sprintf("Docker Engine %s is older than 20.10, which the generated compose files need — upgrade Docker if the install fails", r.DockerVersion))
	}
	info, err := run("docker", "info", "--format", "{{.MemTotal}}|{{.DockerRootDir}}|{{.OperatingSystem}}")
	if err != nil {
		return notes
	}
	parts := strings.SplitN(info, "|", 3)
	if len(parts) != 3 {
		return notes
	}
	if bytes, err := strconv.ParseInt(parts[0], 10, 64); err == nil && bytes > 0 {
		// Round: a 16 GB machine reports a little under 16 GiB.
		r.DockerMemGB = int((bytes + 1<<29) >> 30)
	}
	r.DockerDesktop = strings.Contains(parts[2], "Docker Desktop")
	if gb, ok := freeGB(parts[1]); ok && gb < minDiskGB {
		notes = append(notes, fmt.Sprintf("only %d GB free where Docker keeps images (%s) — the first install downloads a few GB; free some space if the pull fails", gb, parts[1]))
	}
	return notes
}

// freeGB is the free space on the filesystem holding dir, or on its parent
// when dir itself can't be read (Docker's root is root-only); ok=false when
// neither is on this host (Docker Desktop keeps it inside its VM).
func freeGB(dir string) (int, bool) {
	if dir == "" {
		return 0, false
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if b, err := freeBytes(d); err == nil {
			return int(b >> 30), true
		}
	}
	return 0, false
}

// majorMinor parses "29.6.1" / "20.10.24+dfsg1" / "v2.39.1".
func majorMinor(v string) (int, int, bool) {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.TrimLeftFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// PortFree reports whether a container could publish port on this host.
// Docker binds the IPv4 wildcard, and on macOS neither a 127.0.0.1 probe nor
// a dual-stack one collides with that (BSD lets differently-addressed binds
// share a port), so each address family's wildcard is probed on its own. Only
// "in use" counts: a port below 1024 that this user may not bind, or an
// address family the host lacks, is still Docker's to publish.
func PortFree(port int) bool {
	for _, probe := range [][2]string{
		{"tcp4", fmt.Sprintf("127.0.0.1:%d", port)},
		{"tcp4", fmt.Sprintf("0.0.0.0:%d", port)},
		{"tcp6", fmt.Sprintf("[::]:%d", port)},
	} {
		ln, err := net.Listen(probe[0], probe[1])
		if errors.Is(err, syscall.EADDRINUSE) {
			return false
		}
		if err == nil {
			_ = ln.Close()
		}
	}
	return true
}

func detectHost() Host {
	h := Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	h.HasAVX2 = h.Arch == "arm64" || cpuHasAVX2()
	h.RAMGigabytes = ramGB()
	if h.OS == "linux" {
		if b, err := os.ReadFile("/proc/version"); err == nil &&
			strings.Contains(strings.ToLower(string(b)), "microsoft") {
			h.WSL = true
		}
	}
	return h
}
