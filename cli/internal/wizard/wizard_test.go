package wizard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

// Only the bundled Navidrome of a current server can keep its port closed: a
// v0.31 server streams through it, and your own Navidrome isn't ours to publish.
func TestFinishMapsTheNavidromePortChoice(t *testing.T) {
	cases := []struct {
		name     string
		server   string
		publish  bool
		legacy   bool
		wantNoND bool
	}{
		{"bundled, published", "managed", true, false, false},
		{"bundled, kept private", "managed", false, false, true},
		{"bundled on v0.31", "managed", false, true, false},
		{"your Navidrome", "existing", false, false, false},
	}
	for _, c := range cases {
		st := &state{port: "8090", server: c.server, publishNavidrome: c.publish}
		a, _, err := finish(st, deploy.Answers{}, deploy.Secrets{}, preflight.Result{}, Options{LegacyMedia: c.legacy})
		if err != nil {
			t.Fatal(err)
		}
		if a.NoNavidromePort != c.wantNoND {
			t.Errorf("%s: NoNavidromePort = %v, want %v", c.name, a.NoNavidromePort, c.wantNoND)
		}
	}
}

// TestDeriveSMTPTo pins the post-form recipient derivation the wizard relies on
// (huh's Value() snapshots at build time, so SMTP_TO can't be prefilled from the
// admin email typed during the same form — it must be resolved afterwards).
func TestDeriveSMTPTo(t *testing.T) {
	cases := []struct {
		name       string
		entered    string
		adminEmail string
		skipAdmin  bool
		want       string
	}{
		{"blank inherits typed admin email (generate/choose)", "", "op@example.com", false, "op@example.com"},
		{"blank stays blank in skip mode", "", "op@example.com", true, ""},
		{"explicit recipient wins over admin email", "ops@example.com", "op@example.com", false, "ops@example.com"},
		{"explicit recipient wins even in skip mode", "ops@example.com", "op@example.com", true, "ops@example.com"},
		{"no admin email and no entry stays blank", "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveSMTPTo(c.entered, c.adminEmail, c.skipAdmin); got != c.want {
				t.Fatalf("deriveSMTPTo(%q, %q, %v) = %q, want %q", c.entered, c.adminEmail, c.skipAdmin, got, c.want)
			}
		})
	}
}

// A port is checked where it's typed: a number, and free on this host.
func TestPortValidator(t *testing.T) {
	inUse := func(p int) bool { return p == 8090 }
	check := portValidator(inUse)
	for in, want := range map[string]string{"8091": "", " 8091 ": "", "8090": "already in use", "http": "port number", "0": "port number", "70000": "port number"} {
		err := check(in)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
	if err := portValidator(nil)("8090"); err != nil {
		t.Errorf("without a probe any port number goes: %v", err)
	}
}

// The Navidrome port typed when the default was taken lands in NAVIDROME_PORT,
// only when that port is published; the music path comes back absolute.
func TestFinishKeepsTheChosenNavidromePortAndAnAbsoluteLibrary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := &state{port: "8090", server: "managed", publishNavidrome: true, ndPort: "4600", ndPortTaken: true}
	a, _, err := finish(st, deploy.Answers{MusicDir: "~/Music"}, deploy.Secrets{}, preflight.Result{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Keep["NAVIDROME_PORT"] != "4600" {
		t.Errorf("NAVIDROME_PORT = %q", a.Keep["NAVIDROME_PORT"])
	}
	if a.MusicDir != filepath.Join(home, "Music") {
		t.Errorf("music dir %q is not absolute", a.MusicDir)
	}
	st = &state{port: "8090", server: "managed", publishNavidrome: false, ndPort: "4600", ndPortTaken: true}
	if a, _, _ = finish(st, deploy.Answers{MusicDir: "/m"}, deploy.Secrets{}, preflight.Result{}, Options{}); a.Keep["NAVIDROME_PORT"] != "" {
		t.Errorf("a closed port needs no number: %v", a.Keep)
	}
}
