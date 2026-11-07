package deploy

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	return Secrets{AdminPassword: "sct.admin.7f3k",
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
	"sct.admin.7f3k", "sct.amtok.7f3k", "sct.ampw.7f3k",
	"sct.amdb.7f3k", "sct.wg.7f3k", "sct.smtp.7f3k",
}

// fixedPin keeps the golden files still while the manifest's pin moves.
const fixedPin = "ghcr.io/pacholoamit/songstress:v1.0.0"

type matrixCase struct {
	mut func(*Answers)
	pin string // "" = fixedPin
}

func matrix() map[string]matrixCase {
	return map[string]matrixCase{
		"minimal":   {mut: func(a *Answers) {}},
		"discovery": {mut: func(a *Answers) { a.Discovery = true }},
		"noavx2":    {mut: func(a *Answers) { a.Discovery = true; a.NoAVX2 = true }},
		"vpn":       {mut: func(a *Answers) { a.VPN = true }},
		"skip":      {mut: func(a *Answers) { a.SkipAdminSeed = true }},
		"smtp":      {mut: smtpAnswers},
		"skip_smtp": {mut: func(a *Answers) { a.SkipAdminSeed = true; smtpAnswers(a) }},
		"everything": {mut: func(a *Answers) {
			a.Discovery = true
			a.VPN = true
		}},
	}
}

func render(t *testing.T, c matrixCase) Rendered {
	t.Helper()
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	m.Images["songstress"] = fixedPin
	if c.pin != "" {
		m.Images["songstress"] = c.pin
	}
	a := baseAnswers()
	c.mut(&a)
	s := fixedSecrets()
	if !a.Discovery {
		// A fresh install mints AudioMuse's secrets only with Discovery on.
		s.AudioMuseToken, s.AudioMusePassword, s.AudioMuseDB = "", "", ""
	}
	r, err := Generate(a, s, m)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGenerateMatrixGolden(t *testing.T) {
	for name, c := range matrix() {
		t.Run(name, func(t *testing.T) {
			r := render(t, c)
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
	for _, vpn := range []bool{false, true} {
		a := baseAnswers()
		a.Discovery, a.VPN = true, vpn
		smtpAnswers(&a) // exercise the SMTP block so its password sentinel is guarded too
		r, err := Generate(a, fixedSecrets(), m)
		if err != nil {
			t.Fatal(err)
		}
		env := string(r.Files[".env"])
		for _, secret := range sentinelSecrets {
			if !strings.Contains(env, secret) && (vpn || secret != "sct.wg.7f3k") {
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
		if vpn && strings.Contains(string(r.Files["compose.yaml"]), "${SONGSTRESS_PORT}:8090") {
			t.Fatal("with VPN on, songstress shares gluetun's netns — the port must be published there, not on the base service")
		}
	}
}

// One container serves the library: no other music server is generated, and
// nothing reads one's sign-in. What an install from before kept is still there
// for the first start's import (data/navidrome, read in place).
func TestOneContainerServesTheLibrary(t *testing.T) {
	topLevel := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):$`)
	for name, c := range matrix() {
		r := render(t, c)
		for f, content := range r.Files {
			for _, gone := range []string{"${NAVIDROME_PASSWORD", "${NAVIDROME_URL", "${NAVIDROME_USERNAME",
				"SONGSTRESS_MANAGED_NAVIDROME", "navidrome-init", "deluan/navidrome", ":4533"} {
				if strings.Contains(string(content), gone) {
					t.Errorf("%s: %s still has %s", name, f, gone)
				}
			}
		}
		for key := range ParseEnv(r.Files[".env"]) {
			if strings.HasPrefix(key, "NAVIDROME_") {
				t.Errorf("%s: .env writes %s", name, key)
			}
		}
		compose := string(r.Files["compose.yaml"])
		_, services, _ := strings.Cut(compose, "\nservices:\n")
		if got := topLevel.FindAllStringSubmatch(services, -1); len(got) != 1 || got[0][1] != "songstress" {
			t.Errorf("%s: compose.yaml must define the songstress service alone, has %v", name, got)
		}
		mustContain(t, compose, "- NAVIDROME_DB_PATH=${NAVIDROME_DB_PATH:-/pb/pb_data/navidrome/navidrome.db}")
		mustContain(t, compose, "- ./data:/pb/pb_data")
	}
}

// AudioMuse reads the library through Songstress's own OpenSubsonic API, on
// the compose network — or on gluetun's, which Songstress shares with the VPN.
func TestDiscoveryReadsTheLibraryFromSongstress(t *testing.T) {
	for _, c := range []struct {
		vpn  bool
		host string
	}{{false, "songstress"}, {true, "gluetun"}} {
		overlay := string(render(t, matrixCase{mut: func(a *Answers) { a.Discovery, a.VPN = true, c.vpn }}).Files["compose.discovery.yaml"])
		if n := strings.Count(overlay, "- NAVIDROME_URL=http://"+c.host+":8090\n"); n != 2 {
			t.Errorf("both AudioMuse services must read http://%s:8090 (found %d)", c.host, n)
		}
		mustContain(t, overlay, "- NAVIDROME_USER=${SONGSTRESS_ADMIN_EMAIL}")
		mustContain(t, overlay, "- NAVIDROME_PASSWORD=${SONGSTRESS_ADMIN_PASSWORD}")
	}
}

// Discovery turned off keeps AudioMuse's secrets in .env: its database (the
// audiomuse-pgdata volume outlives the overlay) and its admin took them on
// their first boot, so turning it back on must reuse them.
func TestDiscoveryOffKeepsAudioMuseSecrets(t *testing.T) {
	m, _ := LoadManifest()
	r, err := Generate(baseAnswers(), fixedSecrets(), m)
	if err != nil {
		t.Fatal(err)
	}
	env := ParseEnv(r.Files[".env"])
	for key, want := range map[string]string{"AUDIOMUSE_API_TOKEN": "sct.amtok.7f3k",
		"AUDIOMUSE_PASSWORD": "sct.ampw.7f3k", "AUDIOMUSE_DB_PASSWORD": "sct.amdb.7f3k"} {
		if env[key] != want {
			t.Errorf("with Discovery off .env has %s=%q, want the kept %q", key, env[key], want)
		}
	}
	mustContain(t, string(r.Files[".env"]), "# Discovery is off.")
	if _, ok := r.Files["compose.discovery.yaml"]; ok {
		t.Error("Discovery is off: no overlay")
	}
	if fresh := render(t, matrix()["minimal"]); strings.Contains(string(fresh.Files[".env"]), "AUDIOMUSE") {
		t.Error("an install that never had Discovery writes no AudioMuse secrets")
	}
}

// The acquisition providers' keys are optional overrides of the image's
// built-in values: compose forwards all three by name, a value in .env is kept
// by a re-run and one given again replaces it, and no file but .env, and no dry
// run, ever holds a value.
func TestProviderKeysAreForwardedKeptAndNeverShown(t *testing.T) {
	prev := Existing{Env: map[string]string{
		"SONGSTRESS_COMMUNITY_API_KEY": "sct.community.7f3k",
		"SONGSTRESS_QOBUZ_APP_ID":      "sct.qobuzid.7f3k",
	}}
	given := fixedSecrets()
	given.Extra = map[string]string{"SONGSTRESS_COMMUNITY_API_KEY": "sct.community2.7f3k", "SONGSTRESS_QOBUZ_APP_SECRET": "sct.qobuzsecret.7f3k"}
	want := map[string]string{
		"SONGSTRESS_COMMUNITY_API_KEY": "sct.community2.7f3k",
		"SONGSTRESS_QOBUZ_APP_ID":      "sct.qobuzid.7f3k",
		"SONGSTRESS_QOBUZ_APP_SECRET":  "sct.qobuzsecret.7f3k",
	}
	m, _ := LoadManifest()
	s := MergeSecrets(prev.Secrets(), given)
	r, err := Generate(baseAnswers(), s, m)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := Generate(baseAnswers(), s.Redacted(), m)
	if err != nil {
		t.Fatal(err)
	}
	env := ParseEnv(r.Files[".env"])
	compose := string(r.Files["compose.yaml"])
	for _, name := range ProviderKeys {
		value := want[name]
		if env[name] != value {
			t.Errorf(".env has %s=%q, want %q", name, env[name], value)
		}
		if !strings.Contains(compose, "- "+name+"=${"+name+":-}") {
			t.Errorf("compose doesn't forward %s", name)
		}
		for f, content := range shown.Files {
			if strings.Contains(string(content), value) {
				t.Errorf("a dry run shows %s in %s", name, f)
			}
		}
		for f, content := range r.Files {
			if f != ".env" && strings.Contains(string(content), value) {
				t.Errorf("%s holds %s's value; only .env may", f, name)
			}
		}
	}
	if strings.Contains(string(r.Files[".env"]), "sct.community.7f3k") {
		t.Error("the replaced community key is still in .env")
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
		mustContain(t, on, "SMTP_FROM='Songstress <no-reply@example.com>'") // quoted: < > and spaces
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
	if _, err := Generate(a, Secrets{AdminPassword: "y"}, m); err == nil {
		t.Fatal("expected vpn key validation error")
	}
	a = baseAnswers()
	a.MusicDir = ""
	if _, err := Generate(a, fixedSecrets(), m); err == nil {
		t.Fatal("expected a music dir to be required")
	}
}

// TestGoldensAreValidCompose renders each matrix case into a temp dir and
// asks the real `docker compose config` to validate it. Skipped when the
// docker CLI is unavailable.
func TestGoldensAreValidCompose(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	for name, c := range matrix() {
		t.Run(name, func(t *testing.T) {
			r := render(t, c)
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
