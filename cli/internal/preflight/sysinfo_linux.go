//go:build linux

package preflight

import "syscall"

// freeBytes is the space an unprivileged user can still write on dir's filesystem.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
