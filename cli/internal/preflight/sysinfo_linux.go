//go:build linux

package preflight

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func ramGB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return kb >> 20
			}
		}
	}
	return 0
}

func cpuHasAVX2() bool {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		// Fail open: worst case the user overrides the AudioMuse image variant.
		return true
	}
	return strings.Contains(string(b), "avx2")
}

// freeBytes is the space an unprivileged user can still write on dir's filesystem.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
