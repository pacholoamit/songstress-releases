//go:build darwin

package preflight

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func ramGB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	b, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return int(b >> 30)
}

// Apple Silicon and every Intel Mac that can run current Docker Desktop has AVX2.
func cpuHasAVX2() bool { return true }

// freeBytes is the space an unprivileged user can still write on dir's filesystem.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
