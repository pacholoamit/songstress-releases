package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's hand-written composes must name the image the CLI pins, and —
// once that pin is v0.32+ — must stop documenting what those servers dropped
// (stream redirects to Navidrome, APP_VERSION from compose). So a manifest bump
// can't leave the docs describing a different server than the one installed.
func TestReadmeMatchesThePin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Skipf("README.md not beside cli/: %v", err)
	}
	readme := string(b)
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	pin := m.Images["songstress"]
	for _, ref := range regexp.MustCompile(`ghcr\.io/pacholoamit/songstress:[A-Za-z0-9._-]+`).FindAllString(readme, -1) {
		if ref != pin {
			t.Errorf("README pins %s, the CLI installs %s", ref, pin)
		}
	}
	if !strings.Contains(readme, pin) {
		t.Errorf("README never shows the pinned image %s", pin)
	}
	if LegacyServer(pin) {
		return
	}
	for _, gone := range []string{"NAVIDROME_PUBLIC_URL", "SONGSTRESS_NAVIDROME_PORT", "APP_VERSION="} {
		if strings.Contains(readme, gone) {
			t.Errorf("README still documents %s, which %s no longer uses", gone, pin)
		}
	}
}
