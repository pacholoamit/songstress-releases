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
	for _, k := range []string{"songstress", "navidrome", "audiomuse", "audiomuse_noavx2", "postgres", "redis", "gluetun", "caddy", "tailscale"} {
		if m.Images[k] == "" {
			t.Fatalf("manifest missing image %q", k)
		}
	}
	if !strings.Contains(m.Images["songstress"], "ghcr.io/pacholoamit/songstress") {
		t.Fatalf("unexpected songstress image: %s", m.Images["songstress"])
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
