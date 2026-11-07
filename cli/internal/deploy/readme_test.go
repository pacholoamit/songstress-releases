package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's hand-written composes must name the image the CLI pins, and
// must not document what the server dropped (APP_VERSION from compose, the
// sign-in of a music server beside it). So a manifest bump can't leave the docs
// describing a different server than the one installed.
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
	for _, gone := range []string{"NAVIDROME_PUBLIC_URL", "SONGSTRESS_NAVIDROME_PORT", "APP_VERSION=", "NAVIDROME_PASSWORD",
		"SONGSTRESS_MANAGED_NAVIDROME", "deluan/navidrome"} {
		if strings.Contains(readme, gone) {
			t.Errorf("README still documents %s, which %s no longer uses", gone, pin)
		}
	}
}

// Every setting an operator can add to the generated .env — each variable the
// compose for a current server reads that the installer doesn't write itself —
// is documented in the README, as the server's env contract requires of its
// own README.
func TestReadmeDocumentsEverySetting(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md"))
	if err != nil {
		t.Skipf("README.md not beside cli/: %v", err)
	}
	readme := string(b)
	interpolated := regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)`)
	seen := map[string]bool{}
	for name, content := range render(t, matrix()["everything"]).Files {
		if name == ".env" {
			continue
		}
		for _, m := range interpolated.FindAllStringSubmatch(string(content), -1) {
			v := m[1]
			if seen[v] || templateKeys()[v] {
				continue
			}
			seen[v] = true
			if !strings.Contains(readme, "`"+v+"`") && !strings.Contains(readme, v+"=") {
				t.Errorf("README never documents %s, which the generated compose reads from .env", v)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no optional settings — is the parse broken?")
	}
}
