package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// flagsWith is a valid --yes flag set rooted in temp dirs (--music-dir must exist).
func flagsWith(t *testing.T, mut func(*installFlags)) installFlags {
	t.Helper()
	f := installFlags{Yes: true, MusicDir: t.TempDir(), InstallDir: t.TempDir(), Port: 8090, TZ: "UTC"}
	if mut != nil {
		mut(&f)
	}
	return f
}

func TestComponentsFlagParsing(t *testing.T) {
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) {
		f.Components, f.WGPrivateKey = "discovery,vpn", "k"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !a.Discovery || !a.VPN {
		t.Fatalf("bad components: %+v", a)
	}
}

func TestYesRequiresMusicDir(t *testing.T) {
	_, err := answersFromFlags(installFlags{Yes: true, InstallDir: "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "--music-dir") {
		t.Fatalf("expected music-dir error, got %v", err)
	}
}

// --yes used to accept any --music-dir; a typo then bind-mounted an empty
// directory Docker created, and the library silently came up empty.
func TestYesRequiresAnExistingMusicDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.MusicDir = missing }))
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("expected a missing music dir to be refused, got %v", err)
	}
}

// Access networking is bring-your-own: the CLI no longer generates Caddy/HTTPS
// or Tailscale, so those component names must not quietly resurface.
func TestAccessNetworkingComponentsRejected(t *testing.T) {
	for _, c := range []string{"https", "tailscale"} {
		_, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.Components = c }))
		if err == nil || !strings.Contains(err.Error(), "unknown component") {
			t.Fatalf("expected %q to be rejected as unknown, got %v", c, err)
		}
	}
}

func TestVPNRequiresKey(t *testing.T) {
	// The key itself is checked by deploy.Generate; flags only parse the component.
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.Components = "vpn" }))
	if err != nil || !a.VPN {
		t.Fatalf("got %+v, %v", a, err)
	}
}

func TestUnknownComponentRejected(t *testing.T) {
	_, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.Components = "sparkles" }))
	if err == nil || !strings.Contains(err.Error(), "unknown component") {
		t.Fatalf("expected unknown component error, got %v", err)
	}
}

func TestAdminPasswordFlagDoesNotSkip(t *testing.T) {
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.AdminPassword = "correcthorse10" }))
	if err != nil {
		t.Fatal(err)
	}
	if a.SkipAdminSeed {
		t.Fatal("--admin-password must imply choose, not skip")
	}
}

func TestSkipAdminSeedFlag(t *testing.T) {
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.SkipAdminSeed = true }))
	if err != nil {
		t.Fatal(err)
	}
	if !a.SkipAdminSeed {
		t.Fatal("expected SkipAdminSeed=true")
	}
}

func TestAdminPasswordAndSkipMutuallyExclusive(t *testing.T) {
	_, err := answersFromFlags(flagsWith(t, func(f *installFlags) {
		f.AdminPassword, f.SkipAdminSeed = "correcthorse10", true
	}))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestAdminPasswordTooShort(t *testing.T) {
	_, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.AdminPassword = "short" }))
	if err == nil || !strings.Contains(err.Error(), "at least 10") {
		t.Fatalf("expected length error, got %v", err)
	}
}

func TestExistingNavidromeFlags(t *testing.T) {
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) {
		f.NavidromeURL, f.NavidromeUsername = "http://localhost:4533/", "you"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if a.ManagedNavidrome() || a.NavidromeURL != "http://host.docker.internal:4533" || !a.NavidromeHostGateway {
		t.Fatalf("localhost must become host.docker.internal for the container: %+v", a)
	}
	_, err = answersFromFlags(flagsWith(t, func(f *installFlags) { f.NavidromeURL = "http://nd:4533" }))
	if err == nil || !strings.Contains(err.Error(), "--navidrome-username") {
		t.Fatalf("expected the username to be required, got %v", err)
	}
	_, err = answersFromFlags(flagsWith(t, func(f *installFlags) {
		f.NavidromeURL, f.NavidromeUsername = "nd:4533", "you"
	}))
	if err == nil || !strings.Contains(err.Error(), "full URL") {
		t.Fatalf("expected a URL error, got %v", err)
	}
}

func TestContainerNavidromeURL(t *testing.T) {
	cases := map[string]struct {
		want string
		gw   bool
	}{
		"http://192.168.1.10:4533":         {"http://192.168.1.10:4533", false},
		"http://127.0.0.1:4533":            {"http://host.docker.internal:4533", true},
		"https://music.example.com/":       {"https://music.example.com", false},
		"http://host.docker.internal:4533": {"http://host.docker.internal:4533", true},
	}
	for in, c := range cases {
		got, gw, err := containerNavidromeURL(in)
		if err != nil || got != c.want || gw != c.gw {
			t.Errorf("%s → %q gw=%v err=%v, want %q gw=%v", in, got, gw, err, c.want, c.gw)
		}
	}
	if hostView("http://host.docker.internal:4533", true) != "http://127.0.0.1:4533" {
		t.Error("the installer checks a host-gateway URL via 127.0.0.1")
	}
}

// `songstress install --yes` on an existing install reproduces it: flags the
// operator didn't pass come from the lock, flags they did pass win.
func TestInheritAnswersFromTheLock(t *testing.T) {
	prev := deploy.Answers{MusicDir: "/srv/music", Port: 9000, TZ: "Asia/Manila", Discovery: true,
		AdminEmail: "me@x.y", Telemetry: false, NavidromeURL: "http://nd:4533", NavidromeUsername: "you",
		PublicURL: "https://music.example.com"}
	f := installFlags{Port: 8090, TZ: "UTC", AdminEmail: "admin@songstress.local", Telemetry: true, Components: "vpn"}
	changed := map[string]bool{"components": true}
	inheritAnswers(&f, prev, func(name string) bool { return changed[name] })
	if f.MusicDir != "/srv/music" || f.Port != 9000 || f.TZ != "Asia/Manila" || f.AdminEmail != "me@x.y" ||
		f.Telemetry || f.NavidromeURL != "http://nd:4533" || f.PublicURL != "https://music.example.com" {
		t.Fatalf("not inherited: %+v", f)
	}
	if f.Components != "vpn" {
		t.Fatalf("an explicit flag must win: %q", f.Components)
	}
}

// A re-run keeps what the running stack depends on: the file owner, the admin
// mode and email (the app admin already exists), hand-edited .env values.
func TestCarryOverOnRerun(t *testing.T) {
	prev := deploy.Existing{
		HasLock: true,
		Lock: deploy.Lock{Answers: deploy.Answers{PUID: 501, PGID: 20, SkipAdminSeed: true,
			AdminEmail: "first@x.y", SMTPHost: "smtp.x.y", SMTPPort: 587}},
		Env: map[string]string{"SONGSTRESS_PUBLIC_URL": "https://m.x.y", "LAN_SUBNET": "10.0.0.0/8"},
	}
	a := deploy.Answers{PUID: 0, PGID: 0, AdminEmail: "other@x.y"}
	carryOver(&a, prev, true)
	if a.PUID != 501 || a.PGID != 20 || !a.SkipAdminSeed || a.AdminEmail != "first@x.y" {
		t.Fatalf("owner/admin not kept: %+v", a)
	}
	if a.SMTPHost != "smtp.x.y" || a.PublicURL != "https://m.x.y" || a.Keep["LAN_SUBNET"] != "10.0.0.0/8" {
		t.Fatalf("settings not carried: %+v", a)
	}
}

func TestWriteDeploymentPreparesNavidromeData(t *testing.T) {
	dir := t.TempDir()
	// A .env left world-readable by hand must come back 0600.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := deploy.Answers{InstallDir: dir, PUID: os.Getuid(), PGID: os.Getgid()}
	r := deploy.Rendered{Files: map[string][]byte{".env": []byte("A=1\n"), "compose.yaml": []byte("services: {}\n")}}
	if err := writeDeployment(a, r); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "data", "navidrome")); err != nil || !st.IsDir() {
		t.Fatalf("data/navidrome must exist before the first `up`: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(dir, ".env")); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v", st.Mode().Perm())
	}

	existing := t.TempDir()
	a = deploy.Answers{InstallDir: existing, PUID: os.Getuid(), PGID: os.Getgid(), NavidromeURL: "http://nd:4533"}
	if err := writeDeployment(a, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(existing, "data", "navidrome")); !os.IsNotExist(err) {
		t.Fatal("an existing Navidrome needs no data/navidrome")
	}
}

func TestEnsureDirRefusesSomeoneElsesDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown anything")
	}
	dir := t.TempDir()
	err := ensureDir(dir, os.Getuid()+1, os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "sudo chown") {
		t.Fatalf("expected an ownership error with the fix, got %v", err)
	}
}
