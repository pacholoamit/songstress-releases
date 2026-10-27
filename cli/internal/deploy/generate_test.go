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

// existingAnswers is EXISTING mode: the operator's own Navidrome.
func existingAnswers(a *Answers) {
	a.NavidromeURL = "http://192.168.1.10:4533"
	a.NavidromeUsername = "you"
	a.NavidromePublicURL = "http://192.168.1.10:4533"
}

var sentinelSecrets = []string{
	"sct.nd.7f3k", "sct.admin.7f3k", "sct.amtok.7f3k", "sct.ampw.7f3k",
	"sct.amdb.7f3k", "sct.wg.7f3k", "sct.smtp.7f3k",
}

// modernPin is a songstress release that serves all media itself (v0.32+);
// legacyPin one that redirects streams to Navidrome. The cases pinned to them
// keep both branches of the templates covered whatever the manifest pins.
const (
	modernPin = "ghcr.io/pacholoamit/songstress:v0.32.0"
	legacyPin = "ghcr.io/pacholoamit/songstress:v0.31.0"
)

type matrixCase struct {
	mut func(*Answers)
	pin string // "" = the manifest's songstress pin
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
		"existing": {mut: existingAnswers},
		"existing_localhost": {mut: func(a *Answers) {
			existingAnswers(a)
			a.NavidromeURL = "http://host.docker.internal:4533"
			a.NavidromeHostGateway = true
			a.Discovery = true
		}},
		"modern":          {mut: func(a *Answers) {}, pin: modernPin},
		"modern_existing": {mut: existingAnswers, pin: modernPin},
		"modern_everything": {mut: func(a *Answers) {
			a.Discovery = true
			a.VPN = true
		}, pin: modernPin},
		"modern_private":  {mut: func(a *Answers) { a.NoNavidromePort = true }, pin: modernPin},
		"legacy":          {mut: func(a *Answers) {}, pin: legacyPin},
		"legacy_existing": {mut: existingAnswers, pin: legacyPin},
		"legacy_everything": {mut: func(a *Answers) {
			a.Discovery = true
			a.VPN = true
		}, pin: legacyPin},
	}
}

func render(t *testing.T, c matrixCase) Rendered {
	t.Helper()
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
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
	for _, mode := range []func(*Answers){func(*Answers) {}, existingAnswers} {
		a := baseAnswers()
		a.Discovery, a.VPN = true, true
		smtpAnswers(&a) // exercise the SMTP block so its password sentinel is guarded too
		mode(&a)
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
}

// FRESH mode can't deadlock or silently boot without an admin: Navidrome waits
// for the one-shot that hands data/navidrome to PUID:PGID, and an empty
// NAVIDROME_PASSWORD stops compose instead of booting a Navidrome with no admin.
func TestManagedNavidromeBootsSafely(t *testing.T) {
	compose := string(render(t, matrix()["minimal"]).Files["compose.yaml"])
	for _, want := range []string{
		"navidrome-init:",
		`chown ${PUID}:${PGID} /data`,
		"condition: service_completed_successfully",
		"- NAVIDROME_PASSWORD=${NAVIDROME_PASSWORD:?",
		"- ND_DEVAUTOCREATEADMINPASSWORD=${NAVIDROME_PASSWORD:?",
		"- SONGSTRESS_MANAGED_NAVIDROME=1",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("managed compose missing %q", want)
		}
	}
}

// EXISTING mode runs no Navidrome of its own and waits on none.
func TestExistingModeHasNoNavidromeService(t *testing.T) {
	r := render(t, matrix()["existing"])
	compose := string(r.Files["compose.yaml"])
	for _, banned := range []string{"\n  navidrome:", "navidrome-init", "service_healthy", "ND_DEVAUTOCREATEADMINPASSWORD"} {
		if strings.Contains(compose, banned) {
			t.Fatalf("existing-mode compose contains %q", banned)
		}
	}
	for _, want := range []string{"- SONGSTRESS_MANAGED_NAVIDROME=0", "- NAVIDROME_URL=${NAVIDROME_URL:?"} {
		if !strings.Contains(compose, want) {
			t.Fatalf("existing-mode compose missing %q", want)
		}
	}
	env := string(r.Files[".env"])
	mustContain(t, env, "NAVIDROME_URL=http://192.168.1.10:4533")
	mustContain(t, env, "NAVIDROME_USERNAME=you")
}

// v0.31 and older stream by redirecting players to Navidrome, so the pin
// decides whether Navidrome's port is published (and NAVIDROME_PUBLIC_URL set).
func TestLegacyServerFollowsThePin(t *testing.T) {
	legacy := string(render(t, matrix()["legacy"]).Files["compose.yaml"])
	if !strings.Contains(legacy, `"${NAVIDROME_PORT}:4533"`) || !strings.Contains(legacy, "SONGSTRESS_NAVIDROME_PORT") {
		t.Fatal("a v0.31 pin must publish Navidrome's port for its stream redirects")
	}
	if !strings.Contains(string(render(t, matrix()["legacy_existing"]).Files["compose.yaml"]), "NAVIDROME_PUBLIC_URL") {
		t.Fatal("a v0.31 pin redirects streams to an existing Navidrome's public URL")
	}
	if strings.Contains(string(render(t, matrix()["modern"]).Files["compose.yaml"]), "SONGSTRESS_NAVIDROME_PORT") {
		t.Fatal("v0.32+ serves media itself: it has no SONGSTRESS_NAVIDROME_PORT")
	}
	if strings.Contains(string(render(t, matrix()["modern_existing"]).Files["compose.yaml"]), "NAVIDROME_PUBLIC_URL") {
		t.Fatal("v0.32+ has no NAVIDROME_PUBLIC_URL")
	}
}

// The bundled Navidrome's port is published for other Subsonic apps unless the
// operator opts out, and a v0.31 pin, which streams through it, keeps it
// published either way. An existing Navidrome has nothing of ours to publish.
func TestNavidromePortIsPublishedUnlessOptedOut(t *testing.T) {
	const publish = `- "${NAVIDROME_PORT}:4533"`
	cases := map[string]struct {
		c       matrixCase
		publish bool
	}{
		"current, by default":     {matrix()["modern"], true},
		"current, opted out":      {matrix()["modern_private"], false},
		"v0.31, by default":       {matrix()["legacy"], true},
		"v0.31, opted out anyway": {matrixCase{pin: legacyPin, mut: func(a *Answers) { a.NoNavidromePort = true }}, true},
		"current, your Navidrome": {matrix()["modern_existing"], false},
		"v0.31, your Navidrome":   {matrix()["legacy_existing"], false},
	}
	for name, tc := range cases {
		r := render(t, tc.c)
		compose, env := string(r.Files["compose.yaml"]), ParseEnv(r.Files[".env"])
		if got := strings.Contains(compose, publish); got != tc.publish {
			t.Errorf("%s: compose publishes Navidrome's port = %v, want %v", name, got, tc.publish)
		}
		if got := env["NAVIDROME_PORT"] == "4533"; got != tc.publish {
			t.Errorf("%s: .env NAVIDROME_PORT = %q, want it set = %v", name, env["NAVIDROME_PORT"], tc.publish)
		}
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

// v0.32+ images stamp their own APP_VERSION at build; compose must not override
// it. Older images carry none, so the CLI writes the pinned release's version.
func TestAppVersionOnlyForUnstampedImages(t *testing.T) {
	compose := string(render(t, matrix()["legacy"]).Files["compose.yaml"])
	if want := "- APP_VERSION=" + AppVersion(legacyPin); !strings.Contains(compose, want) {
		t.Fatalf("compose missing %q", want)
	}
	for _, name := range []string{"modern", "modern_existing"} {
		if strings.Contains(string(render(t, matrix()[name]).Files["compose.yaml"]), "APP_VERSION") {
			t.Fatalf("%s: a v0.32+ image names its own version", name)
		}
	}
}

// The acquisition providers' keys (read from v0.32.0) are optional overrides of
// the image's built-in values: compose forwards all four by name, a value in
// .env is kept by a re-run and one given again replaces it, and no file but
// .env, and no dry run, ever holds a value.
func TestProviderKeysAreForwardedKeptAndNeverShown(t *testing.T) {
	prev := Existing{Env: map[string]string{
		"SONGSTRESS_ANTRA_API_KEY":     "sct.antra.7f3k",
		"SONGSTRESS_COMMUNITY_API_KEY": "sct.community.7f3k",
		"SONGSTRESS_QOBUZ_APP_ID":      "sct.qobuzid.7f3k",
	}}
	given := fixedSecrets()
	given.Extra = map[string]string{"SONGSTRESS_ANTRA_API_KEY": "sct.antra2.7f3k", "SONGSTRESS_QOBUZ_APP_SECRET": "sct.qobuzsecret.7f3k"}
	want := map[string]string{
		"SONGSTRESS_ANTRA_API_KEY":     "sct.antra2.7f3k",
		"SONGSTRESS_COMMUNITY_API_KEY": "sct.community.7f3k",
		"SONGSTRESS_QOBUZ_APP_ID":      "sct.qobuzid.7f3k",
		"SONGSTRESS_QOBUZ_APP_SECRET":  "sct.qobuzsecret.7f3k",
	}
	m, _ := LoadManifest()
	m.Images["songstress"] = modernPin
	s := MergeSecrets(prev.Secrets(), given, false)
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
	if strings.Contains(string(r.Files[".env"]), "sct.antra.7f3k") {
		t.Error("the replaced Antra key is still in .env")
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
	if _, err := Generate(a, Secrets{NavidromePassword: "x", AdminPassword: "y"}, m); err == nil {
		t.Fatal("expected vpn key validation error")
	}
	if _, err := Generate(baseAnswers(), Secrets{}, m); err == nil {
		t.Fatal("expected a navidrome password to be required")
	}
	a = baseAnswers()
	a.NavidromeURL = "http://nd:4533"
	if _, err := Generate(a, fixedSecrets(), m); err == nil {
		t.Fatal("expected an existing navidrome to need its username")
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

// TestNavidromeDataIsHandedOverBeforeItStarts: on Linux, Docker creates a
// missing bind-mount source as root, and Navidrome, running as PUID:PGID, could
// never write its database there; songstress waits on its health and would
// never start either.
func TestNavidromeDataIsHandedOverBeforeItStarts(t *testing.T) {
	m, _ := LoadManifest()
	r, err := Generate(baseAnswers(), fixedSecrets(), m)
	if err != nil {
		t.Fatal(err)
	}
	compose := string(r.Files["compose.yaml"])
	mustContain(t, compose, "  navidrome-init:\n")
	mustContain(t, compose, `chown ${PUID}:${PGID} /data`)
	mustContain(t, compose, "      navidrome-init:\n        condition: service_completed_successfully\n")
}
