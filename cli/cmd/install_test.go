package cmd

import (
	"strings"
	"testing"
)

func TestComponentsFlagParsing(t *testing.T) {
	a, err := answersFromFlags(installFlags{
		Yes: true, MusicDir: "/m", InstallDir: "/tmp/x", Port: 8090, TZ: "UTC",
		Components: "discovery,vpn", WGPrivateKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Discovery || !a.VPN || a.HTTPS || a.Tailscale {
		t.Fatalf("bad components: %+v", a)
	}
}

func TestYesRequiresMusicDir(t *testing.T) {
	_, err := answersFromFlags(installFlags{Yes: true, InstallDir: "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "--music-dir") {
		t.Fatalf("expected music-dir error, got %v", err)
	}
}

func TestHTTPSComponentRequiresDomain(t *testing.T) {
	_, err := answersFromFlags(installFlags{Yes: true, MusicDir: "/m", InstallDir: "/tmp/x", Components: "https"})
	if err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatalf("expected domain error, got %v", err)
	}
}

func TestVPNRequiresKey(t *testing.T) {
	_, err := answersFromFlags(installFlags{Yes: true, MusicDir: "/m", InstallDir: "/tmp/x", Components: "vpn"})
	if err == nil || !strings.Contains(err.Error(), "--wg-private-key") {
		t.Fatalf("expected wg key error, got %v", err)
	}
}

func TestUnknownComponentRejected(t *testing.T) {
	_, err := answersFromFlags(installFlags{Yes: true, MusicDir: "/m", InstallDir: "/tmp/x", Components: "sparkles"})
	if err == nil || !strings.Contains(err.Error(), "unknown component") {
		t.Fatalf("expected unknown component error, got %v", err)
	}
}
