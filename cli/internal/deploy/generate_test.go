package deploy

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func baseAnswers() Answers {
	return Answers{InstallDir: "/opt/songstress", MusicDir: "/srv/music", Port: 8090,
		TZ: "UTC", PUID: 1000, PGID: 1000, Telemetry: true, AdminEmail: "a@b.c"}
}

// Sentinel values that cannot collide with variable names in templates.
func fixedSecrets() Secrets {
	return Secrets{NavidromePassword: "sct.nd.7f3k", AdminPassword: "sct.admin.7f3k",
		AudioMuseToken: "sct.amtok.7f3k", AudioMusePassword: "sct.ampw.7f3k", AudioMuseDB: "sct.amdb.7f3k",
		WGPrivateKey: "sct.wg.7f3k", TSAuthKey: "sct.ts.7f3k"}
}

var sentinelSecrets = []string{
	"sct.nd.7f3k", "sct.admin.7f3k", "sct.amtok.7f3k", "sct.ampw.7f3k",
	"sct.amdb.7f3k", "sct.wg.7f3k", "sct.ts.7f3k",
}

func matrix() map[string]func(*Answers) {
	return map[string]func(*Answers){
		"minimal":   func(a *Answers) {},
		"discovery": func(a *Answers) { a.Discovery = true },
		"noavx2":    func(a *Answers) { a.Discovery = true; a.NoAVX2 = true },
		"vpn":       func(a *Answers) { a.VPN = true },
		"https":     func(a *Answers) { a.HTTPS = true; a.Domain = "music.example.com"; a.ACMEEmail = "a@b.c" },
		"tailscale": func(a *Answers) { a.Tailscale = true },
		"everything": func(a *Answers) {
			a.Discovery = true
			a.VPN = true
			a.HTTPS = true
			a.Domain = "m.example.com"
			a.ACMEEmail = "a@b.c"
			a.Tailscale = true
		},
	}
}

func TestGenerateMatrixGolden(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for name, mut := range matrix() {
		t.Run(name, func(t *testing.T) {
			a := baseAnswers()
			mut(&a)
			r, err := Generate(a, fixedSecrets(), m)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for f := range r.Files {
				names = append(names, f)
			}
			sort.Strings(names)
			var buf strings.Builder
			buf.WriteString("### compose-args: " + strings.Join(r.ComposeArgs, " ") + "\n")
			for _, f := range names {
				buf.WriteString("### file: " + f + "\n")
				buf.Write(r.Files[f])
				buf.WriteString("\n")
			}
			golden := filepath.Join("testdata", "golden", name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(buf.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if string(want) != buf.String() {
				t.Fatalf("golden mismatch for %s — review then `go test ./internal/deploy -update`", name)
			}
		})
	}
}

func TestGenerateInvariants(t *testing.T) {
	m, _ := LoadManifest()
	a := baseAnswers()
	a.Discovery, a.VPN, a.HTTPS, a.Tailscale = true, true, true, true
	a.Domain, a.ACMEEmail = "m.example.com", "a@b.c"
	r, err := Generate(a, fixedSecrets(), m)
	if err != nil {
		t.Fatal(err)
	}
	env := string(r.Files[".env"])
	for _, secret := range sentinelSecrets {
		if !strings.Contains(env, secret) {
			t.Fatalf(".env missing secret %s", secret)
		}
	}
	for f, content := range r.Files {
		if f == ".env" {
			continue
		}
		for _, secret := range sentinelSecrets {
			if strings.Contains(string(content), secret) {
				t.Fatalf("%s leaks literal secret %s — must reference ${VAR} from .env", f, secret)
			}
		}
		if strings.Contains(string(content), ":latest") {
			t.Fatalf("%s contains an unpinned :latest image", f)
		}
	}
	if strings.Contains(string(r.Files["compose.yaml"]), "${SONGSTRESS_PORT}:8090") {
		t.Fatal("with HTTPS/VPN on, songstress must not publish its port directly")
	}
}

func TestGenerateValidationErrors(t *testing.T) {
	m, _ := LoadManifest()
	a := baseAnswers()
	a.HTTPS = true // no domain
	if _, err := Generate(a, fixedSecrets(), m); err == nil {
		t.Fatal("expected https validation error")
	}
	a = baseAnswers()
	a.VPN = true
	if _, err := Generate(a, Secrets{NavidromePassword: "x", AdminPassword: "y"}, m); err == nil {
		t.Fatal("expected vpn key validation error")
	}
}

// TestGoldensAreValidCompose renders each matrix case into a temp dir and
// asks the real `docker compose config` to validate it. Skipped when the
// docker CLI is unavailable.
func TestGoldensAreValidCompose(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	m, _ := LoadManifest()
	for name, mut := range matrix() {
		t.Run(name, func(t *testing.T) {
			a := baseAnswers()
			mut(&a)
			r, err := Generate(a, fixedSecrets(), m)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			for f, content := range r.Files {
				if err := os.WriteFile(filepath.Join(dir, f), content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"compose", "--project-directory", dir}, prefixPaths(dir, r.ComposeArgs)...)
			args = append(args, "config")
			out, err := exec.Command("docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("compose config failed for %s: %v\n%s", name, err, out)
			}
		})
	}
}

func prefixPaths(dir string, composeArgs []string) []string {
	out := make([]string, len(composeArgs))
	for i, a := range composeArgs {
		if a == "-f" || strings.HasPrefix(a, "-") {
			out[i] = a
		} else {
			out[i] = filepath.Join(dir, a)
		}
	}
	return out
}
