//go:build darwin

package preflight

import (
	"os/exec"
	"strconv"
	"strings"
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
