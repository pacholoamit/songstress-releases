package deploy

import (
	"strings"
	"testing"
)

func TestManifestLoadsAndHasAllImages(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"songstress", "navidrome", "audiomuse", "audiomuse_noavx2", "postgres", "redis", "gluetun"} {
		if m.Images[k] == "" {
			t.Fatalf("manifest missing image %q", k)
		}
	}
	if !strings.Contains(m.Images["songstress"], "ghcr.io/pacholoamit/songstress") {
		t.Fatalf("unexpected songstress image: %s", m.Images["songstress"])
	}
}

// Every pin names an exact tag: no :latest, no rolling :edge (an edge build of
// main can ship without the acquisition providers a release was tested with).
func TestPinsAreExactTags(t *testing.T) {
	m, _ := LoadManifest()
	for name, ref := range m.Images {
		switch tag := ImageTag(ref); tag {
		case "", "latest", "edge":
			t.Errorf("%s pin %q must name an exact version tag", name, ref)
		}
	}
}

func TestAppVersion(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/pacholoamit/songstress:v0.31.0":                "0.31.0",
		"ghcr.io/pacholoamit/songstress:main-3588196":           "main-3588196",
		"ghcr.io/pacholoamit/songstress:v0.32.0@sha256:5f1a9c0": "0.32.0",
		"localhost:5000/songstress:v1.2.3":                      "1.2.3",
		"songstress:e2e":                                        "e2e",
		"localhost:5000/songstress":                             "dev",
		"ghcr.io/pacholoamit/songstress@sha256:abc":             "dev",
	}
	for ref, want := range cases {
		if got := AppVersion(ref); got != want {
			t.Errorf("AppVersion(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestLegacyServer(t *testing.T) {
	cases := map[string]bool{
		"ghcr.io/pacholoamit/songstress:v0.31.0": true,
		"ghcr.io/pacholoamit/songstress:v0.21.4": true,
		"ghcr.io/pacholoamit/songstress:v0.32.0": false,
		"ghcr.io/pacholoamit/songstress:v1.0.0":  false,
		"ghcr.io/pacholoamit/songstress:edge":    false,
		"songstress:e2e":                         false,
	}
	for ref, want := range cases {
		if got := LegacyServer(ref); got != want {
			t.Errorf("LegacyServer(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestOlderRelease(t *testing.T) {
	older := [][2]string{{"x:v0.31.0", "x:v0.32.0"}, {"x:v0.31.9", "x:v0.32.0"}, {"x:v0.9.0", "x:v0.10.0"}}
	for _, p := range older {
		if !OlderRelease(p[0], p[1]) {
			t.Errorf("%s should be older than %s", p[0], p[1])
		}
	}
	notOlder := [][2]string{{"x:v0.32.0", "x:v0.32.0"}, {"x:v1.0.0", "x:v0.99.9"}, {"x:edge", "x:v0.32.0"}}
	for _, p := range notOlder {
		if OlderRelease(p[0], p[1]) {
			t.Errorf("%s should not be older than %s", p[0], p[1])
		}
	}
}

func TestRandomSecretLengthAndCharset(t *testing.T) {
	s, err := RandomSecret(24)
	if err != nil || len(s) != 24 {
		t.Fatalf("bad secret %q err=%v", s, err)
	}
	for _, r := range s {
		if !strings.ContainsRune(alnum, r) {
			t.Fatalf("unexpected char %q", r)
		}
	}
	if s2, _ := RandomSecret(24); s2 == s {
		t.Fatal("two secrets identical")
	}
}
