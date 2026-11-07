package wizard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

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

// The music path comes back absolute, and the components ticked land in the
// answers (Discovery off a CPU without AVX2 takes its -noavx2 image).
func TestFinishMakesTheLibraryAbsoluteAndMapsTheComponents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := &state{port: "8090", components: []string{"discovery", "vpn"}}
	a, _, err := finish(st, deploy.Answers{MusicDir: "~/Music"}, deploy.Secrets{}, preflight.Result{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.MusicDir != filepath.Join(home, "Music") {
		t.Errorf("music dir %q is not absolute", a.MusicDir)
	}
	if !a.Discovery || !a.VPN || !a.NoAVX2 {
		t.Errorf("components: discovery=%v vpn=%v noavx2=%v", a.Discovery, a.VPN, a.NoAVX2)
	}
	if _, _, err := finish(&state{port: "http"}, deploy.Answers{MusicDir: "/m"}, deploy.Secrets{}, preflight.Result{}, Options{}); err == nil {
		t.Error("a port that isn't a number must be refused")
	}
}
