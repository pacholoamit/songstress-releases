package deploy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Passwords an operator might choose.
var trickyValues = []string{
	"plain",
	"pa$$word #1",
	"it's \"quoted\" $HOME \\ path",
	"Songstress <no-reply@example.com>",
	"semi;colon and spaces",
}

func TestQuoteRoundTripsThroughParseEnv(t *testing.T) {
	for _, v := range trickyValues {
		got := ParseEnv([]byte("K=" + quoteEnv(v) + "\n"))["K"]
		if got != v {
			t.Errorf("round trip of %q gave %q (written as %s)", v, got, quoteEnv(v))
		}
	}
}

// What compose itself reads back must be byte-identical, or a chosen admin or
// SMTP password would fail the sign-in it was given for.
func TestQuotedValuesSurviveCompose(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	dir := t.TempDir()
	var env, compose strings.Builder
	compose.WriteString("services:\n  t:\n    image: busybox\n    environment:\n")
	for i, v := range trickyValues {
		key := "V" + string(rune('A'+i))
		env.WriteString(key + "=" + quoteEnv(v) + "\n")
		compose.WriteString("      - " + key + "=${" + key + "}\n")
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "compose", "--project-directory", dir, "config", "--format", "json").Output()
	if err != nil {
		t.Fatalf("compose config: %v", err)
	}
	var cfg struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	for i, v := range trickyValues {
		key := "V" + string(rune('A'+i))
		// `config` prints a literal $ re-escaped as $$ (its output is compose YAML).
		if got := strings.ReplaceAll(cfg.Services["t"].Environment[key], "$$", "$"); got != v {
			t.Errorf("compose read %s as %q, want %q", key, got, v)
		}
	}
}

func TestParseEnvToleratesHandEdits(t *testing.T) {
	env := ParseEnv([]byte("# comment\n\nexport A=1\nB = two \nC=three # trailing\nD=\"x y\"\nE='z'\nnot a pair\n"))
	want := map[string]string{"A": "1", "B": "two", "C": "three", "D": "x y", "E": "z"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("parsed %v", env)
	}
}

// A re-run keeps every secret a running stack depends on; only values the
// operator may legitimately replace take the new one.
func TestMergeSecretsNeverRotatesWhatTheStackDependsOn(t *testing.T) {
	prev := Secrets{AdminPassword: "admin-old", WGPrivateKey: "wg-old", SMTPPassword: "smtp-old"}
	given := Secrets{AdminPassword: "admin-new", WGPrivateKey: "wg-new", SMTPPassword: "smtp-new"}

	merged := MergeSecrets(prev, given)
	if merged.AdminPassword != "admin-old" {
		t.Fatalf("a re-run rotated a first-boot secret: %+v", merged)
	}
	if merged.WGPrivateKey != "wg-new" || merged.SMTPPassword != "smtp-new" {
		t.Fatalf("operator-replaceable secrets should take the new value: %+v", merged)
	}
	// Nothing given: everything kept.
	if got := MergeSecrets(prev, Secrets{}); !reflect.DeepEqual(got, prev) {
		t.Fatalf("got %+v", got)
	}
	// Fresh install: everything comes from `given`.
	if got := MergeSecrets(Secrets{}, given); !reflect.DeepEqual(got, given) {
		t.Fatalf("got %+v", got)
	}
	// The operator's own settings are carried; one given again takes the new
	// value, and a blank one never erases the old.
	prev.Extra = map[string]string{"LOG_LEVEL": "DEBUG", "SONGSTRESS_COMMUNITY_API_KEY": "community-old"}
	given.Extra = map[string]string{"SONGSTRESS_COMMUNITY_API_KEY": "community-new", "SONGSTRESS_QOBUZ_APP_ID": ""}
	want := map[string]string{"LOG_LEVEL": "DEBUG", "SONGSTRESS_COMMUNITY_API_KEY": "community-new"}
	if got := MergeSecrets(prev, given); !reflect.DeepEqual(got.Extra, want) {
		t.Fatalf("extras = %v, want %v", got.Extra, want)
	}
	if prev.Extra["SONGSTRESS_COMMUNITY_API_KEY"] != "community-old" {
		t.Fatal("merging must not write into the previous install's map")
	}
}

// Redacted must hide every secret field — including any field added later —
// and the operator's own values, and leave an empty one empty.
func TestRedactedHidesEverySecret(t *testing.T) {
	var s Secrets
	v := reflect.ValueOf(&s).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.String {
			f.SetString("sentinel-" + v.Type().Field(i).Name)
		}
	}
	s.Extra = map[string]string{"GLUETUN_API_KEY": "sentinel-extra", "EMPTY": ""}
	r := reflect.ValueOf(s.Redacted())
	for i := 0; i < r.NumField(); i++ {
		if f := r.Field(i); f.Kind() == reflect.String && f.String() != Hidden {
			t.Errorf("Redacted leaves %s = %q", r.Type().Field(i).Name, f.String())
		}
	}
	if extra := s.Redacted().Extra; extra["GLUETUN_API_KEY"] != Hidden || extra["EMPTY"] != "" {
		t.Errorf("Redacted extras = %v", extra)
	}
	if got := (Secrets{}).Redacted(); !reflect.DeepEqual(got, Secrets{}) {
		t.Errorf("empty secrets must stay empty (validation depends on it): %+v", got)
	}
}

// What the operator added to .env by hand survives a re-run byte for byte; what
// the installer writes itself, and what older releases wrote for features since
// removed (Tailscale, the music server beside Songstress), does not come back
// as theirs.
func TestHandAddedSettingsSurviveARerun(t *testing.T) {
	dir := t.TempDir()
	prevEnv := "TZ=UTC\nNAVIDROME_PASSWORD=nd\nNAVIDROME_URL=http://nd:4533\nNAVIDROME_USERNAME=you\n" +
		"NAVIDROME_PORT=4533\nAUDIOMUSE_API_TOKEN=t\nAUDIOMUSE_PASSWORD=p\nAUDIOMUSE_DB_PASSWORD=d\n" +
		"LAN_SUBNET=10.0.0.0/8\nTS_AUTHKEY=tskey-old\n" +
		"# my additions\nLOG_LEVEL=DEBUG\nGLUETUN_API_KEY=" + quoteEnv("k$y #1") + "\nSPOTIFY_CLIENT_ID=abc\nnot a pair\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(prevEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := LoadExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := e.Secrets()
	want := map[string]string{"LOG_LEVEL": "DEBUG", "GLUETUN_API_KEY": "k$y #1", "SPOTIFY_CLIENT_ID": "abc"}
	if !reflect.DeepEqual(s.Extra, want) {
		t.Fatalf("extras = %v, want %v", s.Extra, want)
	}

	m, _ := LoadManifest()
	a := baseAnswers()
	a.VPN = true
	s.AdminPassword, s.WGPrivateKey = "admin", "wg"
	r, err := Generate(a, s, m)
	if err != nil {
		t.Fatal(err)
	}
	env := ParseEnv(r.Files[".env"])
	for k, v := range want {
		if env[k] != v {
			t.Errorf("re-run wrote %s=%q, want %q", k, env[k], v)
		}
	}
	for _, retired := range []string{"TS_AUTHKEY", "NAVIDROME_PASSWORD", "NAVIDROME_URL", "NAVIDROME_USERNAME", "NAVIDROME_PORT",
		"AUDIOMUSE_API_TOKEN", "AUDIOMUSE_PASSWORD", "AUDIOMUSE_DB_PASSWORD"} {
		if _, ok := env[retired]; ok {
			t.Errorf("the retired %s came back", retired)
		}
	}
	if !e.HadMusicServer() {
		t.Error("an .env with a music server's sign-in is an install that had one beside Songstress")
	}
	for f, content := range r.Files {
		if f != ".env" && strings.Contains(string(content), "k$y #1") {
			t.Errorf("%s carries a hand-added value; only .env may", f)
		}
	}
	if len(Existing{}.Secrets().Extra) != 0 {
		t.Error("a fresh install has no hand-added settings")
	}
}

func TestLoadExisting(t *testing.T) {
	dir := t.TempDir()
	e, err := LoadExisting(dir)
	if err != nil || e.Found() {
		t.Fatalf("empty dir: found=%v err=%v", e.Found(), err)
	}
	if err := WriteLock(dir, Lock{Answers: Answers{Port: 9000}}); err != nil {
		t.Fatal(err)
	}
	envFile := "SONGSTRESS_ADMIN_PASSWORD=" + quoteEnv("admin pass$") + "\nLAN_SUBNET=10.0.0.0/8\nSMTP_PASSWORD=x\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, err = LoadExisting(dir); err != nil || !e.Found() || !e.HasLock {
		t.Fatalf("found=%v lock=%v err=%v", e.Found(), e.HasLock, err)
	}
	if e.Secrets().AdminPassword != "admin pass$" || e.Lock.Answers.Port != 9000 || e.HadMusicServer() {
		t.Fatalf("got %+v / %+v", e.Secrets(), e.Lock.Answers)
	}
	if keep := e.Keep(); keep["LAN_SUBNET"] != "10.0.0.0/8" || keep["SMTP_PASSWORD"] != "" {
		t.Fatalf("keep must carry only allow-listed non-secrets: %v", keep)
	}
}
