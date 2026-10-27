package deploy

import (
	"os"
	"path/filepath"
	"strings"
)

// AbsPath is a path as typed ("~/Music", "./music", "/srv/music") made
// absolute. The install and music directories land in .env and compose,
// which resolve a relative path against the install directory, not where the
// installer ran: "./music" became an empty bind mount, and "~/Music" (which a
// shell leaves alone in --music-dir=~/Music, and the wizard always does) was
// refused as missing.
func AbsPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}
