package deploy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Passwords an operator might choose (or an existing Navidrome might have).
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

// What compose itself reads back must be byte-identical, or an existing
// Navidrome's password would fail the sign-in the installer just verified.
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
	prev := Secrets{NavidromePassword: "nd-old", AdminPassword: "admin-old", AudioMuseDB: "db-old",
		AudioMuseToken: "tok-old", AudioMusePassword: "am-old", WGPrivateKey: "wg-old", SMTPPassword: "smtp-old"}
	given := Secrets{NavidromePassword: "nd-new", AdminPassword: "admin-new", AudioMuseDB: "db-new",
		AudioMuseToken: "tok-new", AudioMusePassword: "am-new", WGPrivateKey: "wg-new", SMTPPassword: "smtp-new"}

	managed := MergeSecrets(prev, given, false)
	if managed.NavidromePassword != "nd-old" || managed.AdminPassword != "admin-old" ||
		managed.AudioMuseDB != "db-old" || managed.AudioMuseToken != "tok-old" || managed.AudioMusePassword != "am-old" {
		t.Fatalf("a re-run rotated a first-boot secret: %+v", managed)
	}
	if managed.WGPrivateKey != "wg-new" || managed.SMTPPassword != "smtp-new" {
		t.Fatalf("operator-replaceable secrets should take the new value: %+v", managed)
	}
	// An existing Navidrome's password is the operator's (and gets verified).
	if MergeSecrets(prev, given, true).NavidromePassword != "nd-new" {
		t.Fatal("an existing Navidrome's new password must be used")
	}
	// Nothing given: everything kept.
	if got := MergeSecrets(prev, Secrets{}, true); got != prev {
		t.Fatalf("got %+v", got)
	}
	// Fresh install: everything comes from `given`.
	if got := MergeSecrets(Secrets{}, given, false); got != given {
		t.Fatalf("got %+v", got)
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
	envFile := "NAVIDROME_PASSWORD=" + quoteEnv("nd pass$") + "\nLAN_SUBNET=10.0.0.0/8\nSMTP_PASSWORD=x\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, err = LoadExisting(dir); err != nil || !e.Found() || !e.HasLock {
		t.Fatalf("found=%v lock=%v err=%v", e.Found(), e.HasLock, err)
	}
	if e.Secrets().NavidromePassword != "nd pass$" || e.Lock.Answers.Port != 9000 {
		t.Fatalf("got %+v / %+v", e.Secrets(), e.Lock.Answers)
	}
	if keep := e.Keep(); keep["LAN_SUBNET"] != "10.0.0.0/8" || keep["SMTP_PASSWORD"] != "" {
		t.Fatalf("keep must carry only allow-listed non-secrets: %v", keep)
	}
}
