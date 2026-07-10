// Package preflight probes the host before install: docker + compose v2,
// RAM/AVX2 (drives discovery defaults + image variant), WSL, free ports.
// All command execution goes through Runner so every path is unit-testable.
package preflight

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	ComposeVersion      string
	Host                Host
	PortFree            map[int]bool
	Notes               []string
}

// Run probes the host. It never fails on missing tooling — absence is data
// the install command turns into friendly guidance.
func Run(run Runner, ports []int) (Result, error) {
	r := Result{PortFree: map[int]bool{}}
	r.Host = detectHost()

	if _, err := run("docker", "version", "--format", "{{.Server.Version}}"); err == nil {
		r.DockerOK = true
	}
	if v, err := run("docker", "compose", "version", "--short"); err == nil {
		r.ComposeOK = true
		r.ComposeVersion = v
	}

	for _, p := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			_ = ln.Close()
			r.PortFree[p] = true
		} else {
			r.PortFree[p] = false
			r.Notes = append(r.Notes, fmt.Sprintf("port %d is already in use", p))
		}
	}

	if r.Host.WSL {
		r.Notes = append(r.Notes, "WSL detected — keep the music library on the Linux filesystem (not /mnt/c) for scan performance")
	}
	if r.Host.RAMGigabytes > 0 && r.Host.RAMGigabytes < 8 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d GB RAM — Discovery (AudioMuse) wants ~8 GB during analysis; it will default off", r.Host.RAMGigabytes))
	}
	return r, nil
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
