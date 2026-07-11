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
		WGPrivateKey: "sct.wg.7f3k", SMTPPassword: "sct.smtp.7f3k"}
}

// smtpAnswers sets the non-secret SMTP fields (the password rides in Secrets).
func smtpAnswers(a *Answers) {
	a.SMTPHost = "smtp.example.com"
	a.SMTPPort = 587
	a.SMTPUsername = "postmaster@example.com"
	a.SMTPFrom = "Songstress <no-reply@example.com>"
	a.SMTPTo = "ops@example.com"
	a.SMTPStartTLS = true
}

var sentinelSecrets = []string{
	"sct.nd.7f3k", "sct.admin.7f3k", "sct.amtok.7f3k", "sct.ampw.7f3k",
	"sct.amdb.7f3k", "sct.wg.7f3k", "sct.smtp.7f3k",
}

func matrix() map[string]func(*Answers) {
	return map[string]func(*Answers){
		"minimal":   func(a *Answers) {},
		"discovery": func(a *Answers) { a.Discovery = true },
		"noavx2":    func(a *Answers) { a.Discovery = true; a.NoAVX2 = true },
		"vpn":       func(a *Answers) { a.VPN = true },
		"skip":      func(a *Answers) { a.SkipAdminSeed = true },
		"smtp":      smtpAnswers,
		"skip_smtp": func(a *Answers) { a.SkipAdminSeed = true; smtpAnswers(a) },
		"everything": func(a *Answers) {
			a.Discovery = true
			a.VPN = true
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
	a.Discovery, a.VPN = true, true
	smtpAnswers(&a) // exercise the SMTP block so its password sentinel is guarded too
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
		t.Fatal("with VPN on, songstress shares gluetun's netns — the port must be published there, not on the base service")
	}
}

// TestPortIsAlwaysPublished pins the bring-your-own-networking contract: the CLI
// terminates no TLS and joins no tailnet, so the dashboard port must always
// reach the host for the operator's own proxy/tunnel to front — on the base
// service normally, and on gluetun when songstress shares its netns.
func TestPortIsAlwaysPublished(t *testing.T) {
	m, _ := LoadManifest()
	const publish = "${SONGSTRESS_PORT}:8090"

	a := baseAnswers()
	r, err := Generate(a, fixedSecrets(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.Files["compose.yaml"]), publish) {
		t.Fatal("without VPN, the base compose must publish the dashboard port to the host")
	}

	a = baseAnswers()
	a.VPN = true
	if r, err = Generate(a, fixedSecrets(), m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.Files["compose.vpn.yaml"]), publish) {
		t.Fatal("with VPN, gluetun must publish the dashboard port to the host")
	}
}

// TestAdminAndSMTPEnvBlocks pins the exact .env admin/SMTP blocks per mode,
// independent of the golden snapshots.
func TestAdminAndSMTPEnvBlocks(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	env := func(mut func(*Answers)) string {
		a := baseAnswers()
		mut(&a)
		r, err := Generate(a, fixedSecrets(), m)
		if err != nil {
			t.Fatal(err)
		}
		return string(r.Files[".env"])
	}

	t.Run("seed writes the admin pair", func(t *testing.T) {
		e := env(func(a *Answers) {})
		mustContain(t, e, "SONGSTRESS_ADMIN_EMAIL=a@b.c")
		mustContain(t, e, "SONGSTRESS_ADMIN_PASSWORD=sct.admin.7f3k")
		mustNotContain(t, e, "created in-app")
	})

	t.Run("skip leaves the admin pair empty with a comment", func(t *testing.T) {
		e := env(func(a *Answers) { a.SkipAdminSeed = true })
		mustContain(t, e, "# admin is created in-app on first client connect")
		mustContain(t, e, "SONGSTRESS_ADMIN_EMAIL=\n")
		mustContain(t, e, "SONGSTRESS_ADMIN_PASSWORD=\n")
		mustNotContain(t, e, "sct.admin.7f3k") // no seeded password, even if one was minted
	})

	t.Run("smtp block present only when configured", func(t *testing.T) {
		off := env(func(a *Answers) {})
		mustNotContain(t, off, "SMTP_HOST")
		mustNotContain(t, off, "sct.smtp.7f3k")

		on := env(smtpAnswers)
		mustContain(t, on, "SMTP_HOST=smtp.example.com")
		mustContain(t, on, "SMTP_PORT=587")
		mustContain(t, on, "SMTP_USERNAME=postmaster@example.com")
		mustContain(t, on, "SMTP_PASSWORD=sct.smtp.7f3k")
		mustContain(t, on, "SMTP_FROM=Songstress <no-reply@example.com>")
		mustContain(t, on, "SMTP_TO=ops@example.com")
		mustContain(t, on, "SMTP_STARTTLS=true")
	})
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected .env to contain %q\n---\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected .env NOT to contain %q\n---\n%s", needle, haystack)
	}
}

func TestGenerateValidationErrors(t *testing.T) {
	m, _ := LoadManifest()
	a := baseAnswers()
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
