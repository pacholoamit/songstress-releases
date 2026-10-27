package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

// flagsWith is a valid --yes flag set rooted in temp dirs (--music-dir must exist).
func flagsWith(t *testing.T, mut func(*installFlags)) installFlags {
	t.Helper()
	f := installFlags{Yes: true, MusicDir: t.TempDir(), InstallDir: t.TempDir(), Port: 8090, TZ: "UTC", PublishNavidrome: true}
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
		PublicURL: "https://music.example.com", NoNavidromePort: true}
	f := installFlags{Port: 8090, TZ: "UTC", AdminEmail: "admin@songstress.local", Telemetry: true, Components: "vpn",
		PublishNavidrome: true}
	changed := map[string]bool{"components": true}
	inheritAnswers(&f, prev, func(name string) bool { return changed[name] })
	if f.MusicDir != "/srv/music" || f.Port != 9000 || f.TZ != "Asia/Manila" || f.AdminEmail != "me@x.y" ||
		f.Telemetry || f.NavidromeURL != "http://nd:4533" || f.PublicURL != "https://music.example.com" ||
		f.PublishNavidrome {
		t.Fatalf("not inherited: %+v", f)
	}
	if f.Components != "vpn" {
		t.Fatalf("an explicit flag must win: %q", f.Components)
	}
	// An install that published Navidrome's port (or predates the choice)
	// keeps publishing it; --publish-navidrome given now wins.
	f = installFlags{PublishNavidrome: false}
	inheritAnswers(&f, deploy.Answers{}, func(string) bool { return false })
	if !f.PublishNavidrome {
		t.Fatal("a lock without the choice must keep Navidrome's port published")
	}
	f = installFlags{PublishNavidrome: true}
	inheritAnswers(&f, prev, func(name string) bool { return name == "publish-navidrome" })
	if !f.PublishNavidrome {
		t.Fatal("an explicit --publish-navidrome must win over the lock")
	}
}

func TestPublishNavidromeFlag(t *testing.T) {
	for _, publish := range []bool{true, false} {
		a, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.PublishNavidrome = publish }))
		if err != nil {
			t.Fatal(err)
		}
		if a.NoNavidromePort == publish {
			t.Errorf("--publish-navidrome=%v gave NoNavidromePort=%v", publish, a.NoNavidromePort)
		}
	}
}

// A re-run's own stack holds the ports its .env names; only someone else's
// hold counts as "in use".
func TestPortInUseIgnoresTheInstallsOwnPorts(t *testing.T) {
	prev := deploy.Existing{Env: map[string]string{"SONGSTRESS_PORT": "8090", "NAVIDROME_PORT": "4533"}}
	inUse := portInUse(prev, func(int) bool { return false }) // every port taken
	if inUse(8090) || inUse(4533) {
		t.Fatal("the install's own ports are not in someone else's use")
	}
	if !inUse(9000) {
		t.Fatal("a port the install doesn't hold is in use")
	}
}

// A taken port is refused before anything is written, with the flag that
// fixes it and a port that is free — not left to fail `docker compose up`.
func TestBusyPortsSayHowToFixThem(t *testing.T) {
	taken := map[int]bool{8090: true, 8091: true, 4533: true, 4600: true}
	inUse := func(p int) bool { return taken[p] }
	base := deploy.Answers{Port: 8090}
	cases := []struct {
		name   string
		a      deploy.Answers
		legacy bool
		want   []string
		none   bool
	}{
		{"dashboard and Navidrome", base, false,
			[]string{"port 8090 is already in use", "--port (8092 is free)", "port 4533, for the bundled Navidrome", "--navidrome-port (4534 is free)", "--publish-navidrome=false"}, false},
		{"v0.31 can't close Navidrome's port", deploy.Answers{Port: 9000}, true,
			[]string{"--navidrome-port"}, false},
		{"Navidrome kept private", deploy.Answers{Port: 9000, NoNavidromePort: true}, false, nil, true},
		{"your own Navidrome", deploy.Answers{Port: 9000, NavidromeURL: "http://nd:4533"}, false, nil, true},
		{"a chosen Navidrome port", deploy.Answers{Port: 9000, Keep: map[string]string{"NAVIDROME_PORT": "4600"}}, false,
			[]string{"port 4600"}, false},
		{"a hand-written binding is the operator's", deploy.Answers{Port: 9000, Keep: map[string]string{"NAVIDROME_PORT": "127.0.0.1:4533"}}, false, nil, true},
	}
	for _, c := range cases {
		err := busyPorts(c.a, c.legacy, inUse)
		if c.none {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected a refusal", c.name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", c.name, err, w)
			}
		}
		if c.legacy && strings.Contains(err.Error(), "--publish-navidrome") {
			t.Errorf("%s: v0.31 streams through Navidrome's port; closing it is no fix", c.name)
		}
	}
}

// --navidrome-port lands in NAVIDROME_PORT; without it a re-run keeps the
// port .env names, and with it the new port wins.
func TestNavidromePortFlag(t *testing.T) {
	collect := func(f installFlags, prev deploy.Existing) (deploy.Answers, error) {
		a, _, err := collectAnswers(f, preflight.Result{}, prev, false, false, func(int) bool { return false })
		return a, err
	}
	a, err := collect(flagsWith(t, func(f *installFlags) { f.NavidromePort, f.NavidromePortSet = 4600, true }), deploy.Existing{})
	if err != nil || a.Keep["NAVIDROME_PORT"] != "4600" {
		t.Fatalf("--navidrome-port 4600 gave %v, %v", a.Keep, err)
	}
	if _, err := collect(flagsWith(t, func(f *installFlags) { f.NavidromePort, f.NavidromePortSet = 70000, true }), deploy.Existing{}); err == nil {
		t.Fatal("port 70000 must be refused")
	}
	prev := deploy.Existing{Env: map[string]string{"NAVIDROME_PORT": "4700"}}
	if a, _ := collect(flagsWith(t, nil), prev); a.Keep["NAVIDROME_PORT"] != "4700" {
		t.Fatalf("a re-run must keep .env's port: %v", a.Keep)
	}
	if a, _ := collect(flagsWith(t, func(f *installFlags) { f.NavidromePort, f.NavidromePortSet = 4600, true }), prev); a.Keep["NAVIDROME_PORT"] != "4600" {
		t.Fatalf("--navidrome-port must win over .env: %v", a.Keep)
	}
}

// Without a terminal there's no wizard; say so instead of complaining about a
// --yes flag nobody passed.
func TestNoTerminalAsksForYes(t *testing.T) {
	_, _, err := collectAnswers(installFlags{InstallDir: t.TempDir()}, preflight.Result{}, deploy.Existing{}, false, false, nil)
	if err == nil || !strings.Contains(err.Error(), "no terminal") || !strings.Contains(err.Error(), "--yes with --music-dir") {
		t.Fatalf("got %v", err)
	}
}

// "./music" and "~/Music" name the directory the operator means: .env gets it
// absolute (compose would read a relative path against the install dir).
func TestMusicDirIsMadeAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "Music"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	for in, want := range map[string]string{"~/Music": filepath.Join(home, "Music"), "./music": filepath.Join(work, "music")} {
		a, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.MusicDir = in }))
		got, _ := filepath.EvalSymlinks(a.MusicDir)
		real, _ := filepath.EvalSymlinks(want)
		if err != nil || got != real {
			t.Errorf("--music-dir %s → %q (%v), want %q", in, a.MusicDir, err, want)
		}
	}
	f := installFlags{InstallDir: "./songstress", MusicDir: "~/Music"}
	if err := resolveDirs(&f, false); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(f.InstallDir) || f.MusicDir != filepath.Join(home, "Music") {
		t.Fatalf("resolveDirs left %+v", f)
	}
}

// A re-run says what it rewrites, and an overlay whose component was turned
// off is removed with it — only one the installer generated.
func TestRerunReportsAndRemovesWhatChanged(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "same\n")
	write(".env", "old\n")
	write("compose.discovery.yaml", generatedHeader+" — Discovery overlay\n")
	write("compose.vpn.yaml", "# my own file\n")
	files := map[string][]byte{"compose.yaml": []byte("same\n"), ".env": []byte("new\n")}
	got := changedFiles(dir, files)
	if !reflect.DeepEqual(got, []string{".env", "compose.discovery.yaml (removed)"}) {
		t.Fatalf("changed = %v", got)
	}
	if d := describeChanges(got); d != "rewriting .env, compose.discovery.yaml (removed)" {
		t.Fatalf("describe = %q", d)
	}
	if d := describeChanges(nil); !strings.Contains(d, "unchanged") {
		t.Fatalf("describe = %q", d)
	}
	a := deploy.Answers{InstallDir: dir, PUID: os.Getuid(), PGID: os.Getgid(), NavidromeURL: "http://nd:4533"}
	if err := writeDeployment(a, deploy.Rendered{Files: files}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.discovery.yaml")); !os.IsNotExist(err) {
		t.Fatal("the stale Discovery overlay should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.vpn.yaml")); err != nil {
		t.Fatal("a file the installer didn't generate must be left alone")
	}
}

// The success screen tells a newcomer where to go from another device, how to
// see the generated password, and how to update later.
func TestSummaryTellsWhatToDoNext(t *testing.T) {
	a := deploy.Answers{InstallDir: "/srv/songstress", MusicDir: "/srv/music", Port: 8090, AdminEmail: "me@x.y"}
	var out strings.Builder
	printSummary(&out, a, outcome{ndPublished: true, lan: "192.168.1.20"})
	for _, want := range []string{
		"open http://localhost:8090 (from another device: http://192.168.1.20:8090)",
		"grep SONGSTRESS_ADMIN_PASSWORD= /srv/songstress/.env",
		"desktop & mobile apps: enter http://192.168.1.20:8090",
		"on port 4533",
		"re-run `songstress install --install-dir /srv/songstress`",
		installURL,
		"--public-url",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	a.Keep = map[string]string{"NAVIDROME_PORT": "4600"}
	printSummary(&out, a, outcome{ndPublished: true, rerun: true, keptAdmin: true, defaultDir: true})
	for _, want := range []string{"is updated and up", "your sign-in is unchanged (me@x.y", "on port 4600", "re-run `songstress install` to"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("re-run summary lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "grep") {
		t.Error("a re-run has nothing new to show")
	}
}

// Scripted, a rejected Navidrome login stops the install with the reason and
// what to check; nothing is asked.
func TestExistingNavidromeRefusalSaysWhatToCheck(t *testing.T) {
	nd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`)
	}))
	defer nd.Close()
	a := deploy.Answers{NavidromeURL: nd.URL, NavidromeUsername: "you"}
	s := deploy.Secrets{NavidromePassword: "wrong"}
	var out strings.Builder
	err := checkExistingNavidrome(context.Background(), &out, &a, &s, false, false)
	if err == nil || !strings.Contains(err.Error(), "not installing") || !strings.Contains(err.Error(), "rejected the username or password") ||
		!strings.Contains(err.Error(), "check the username and password") {
		t.Fatalf("got %v", err)
	}
}

// As root, a fresh install runs the containers as the library's owner: files
// Songstress writes into it must not become root's. Anyone else runs as
// themselves.
func TestFileOwner(t *testing.T) {
	music := t.TempDir()
	info, err := os.Stat(music)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if uid, gid := fileOwner(0, 0, music); st.Uid != 0 && (uid != int(st.Uid) || gid != int(st.Gid)) {
		t.Fatalf("root got %d:%d, want the library owner's %d:%d", uid, gid, st.Uid, st.Gid)
	}
	if uid, gid := fileOwner(1000, 1000, music); uid != 1000 || gid != 1000 {
		t.Fatalf("a user runs as themselves, got %d:%d", uid, gid)
	}
	if uid, _ := fileOwner(0, 0, filepath.Join(music, "missing")); uid != 0 {
		t.Fatal("no library, no one else to run as")
	}
}

// A URL without a port reaches port 80, where Navidrome rarely is: say which
// port it listens on.
func TestUnreachableNavidromeWithoutAPortSuggestsOne(t *testing.T) {
	// .invalid never resolves (RFC 2606), so nothing can answer it.
	a := deploy.Answers{NavidromeURL: "http://navidrome.invalid", NavidromeUsername: "you"}
	s := deploy.Secrets{NavidromePassword: "pw"}
	var out strings.Builder
	problem := navidromeProblem(context.Background(), &out, a, s)
	if !strings.Contains(problem, "http://navidrome.invalid:4533?") {
		t.Fatalf("expected a :4533 suggestion, got %q", problem)
	}
}

func TestMusicDirNote(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if n := musicDirNote(t.TempDir()); n != "" {
		t.Fatalf("a writable library needs no note: %q", n)
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if n := musicDirNote(ro); !strings.Contains(n, "isn't writable") {
		t.Fatalf("a read-only library should be noted: %q", n)
	}
}

// Turning Discovery off and back on must not lock AudioMuse out of its own
// database: Postgres and AudioMuse's admin take these passwords on their first
// boot only, and the audiomuse-pgdata volume outlives the overlay. Each run
// goes the way runInstall goes: read the install, merge, mint, render.
func TestDiscoveryOffAndOnKeepsAudioMusePasswords(t *testing.T) {
	m, err := deploy.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	install := func(discovery bool) map[string]string {
		t.Helper()
		prev, err := deploy.LoadExisting(dir)
		if err != nil {
			t.Fatal(err)
		}
		a := deploy.Answers{InstallDir: dir, MusicDir: "/srv/music", Port: 8090, TZ: "UTC",
			PUID: 1000, PGID: 1000, AdminEmail: "a@b.c", Discovery: discovery}
		s := deploy.MergeSecrets(prev.Secrets(), deploy.Secrets{}, false)
		if err := mintSecrets(&s, a); err != nil {
			t.Fatal(err)
		}
		r, err := deploy.Generate(a, s, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), r.Files[".env"], 0o600); err != nil {
			t.Fatal(err)
		}
		return deploy.ParseEnv(r.Files[".env"])
	}
	keys := []string{"AUDIOMUSE_API_TOKEN", "AUDIOMUSE_PASSWORD", "AUDIOMUSE_DB_PASSWORD"}

	if first := install(false); first["AUDIOMUSE_DB_PASSWORD"] != "" {
		t.Fatal("no Discovery yet: nothing to mint for it")
	}
	on := install(true)
	off := install(false)
	again := install(true)
	for _, k := range keys {
		if on[k] == "" {
			t.Fatalf("Discovery on: %s was never minted", k)
		}
		if off[k] != on[k] || again[k] != on[k] {
			t.Errorf("%s: on %q, off %q, on again %q — it must never change", k, on[k], off[k], again[k])
		}
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

// A dry run shows every file but no secret: on a re-run they are the live ones,
// and terminal scrollback and CI logs keep whatever is printed.
func TestDryRunPrintsNoSecret(t *testing.T) {
	m, err := deploy.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	a := deploy.Answers{InstallDir: "/opt/songstress", MusicDir: "/srv/music", Port: 8090, TZ: "UTC",
		PUID: 1000, PGID: 1000, AdminEmail: "a@b.c", Discovery: true, VPN: true, SMTPHost: "smtp.x.y", SMTPPort: 587}
	secrets := []string{"nd-7f3k", "admin-7f3k", "amtok-7f3k", "ampw-7f3k", "amdb-7f3k", "wg-7f3k", "smtp-7f3k", "own-7f3k"}
	s := deploy.Secrets{NavidromePassword: secrets[0], AdminPassword: secrets[1], AudioMuseToken: secrets[2],
		AudioMusePassword: secrets[3], AudioMuseDB: secrets[4], WGPrivateKey: secrets[5], SMTPPassword: secrets[6],
		Extra: map[string]string{"GLUETUN_API_KEY": secrets[7]}}
	var out strings.Builder
	if err := printDryRun(&out, a, s, m); err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the dry run printed %s", secret)
		}
	}
	for _, want := range []string{"compose.yaml", "compose.vpn.yaml", "SONGSTRESS_ADMIN_PASSWORD='" + deploy.Hidden + "'", "GLUETUN_API_KEY="} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
}

// A provider key in the installer's environment becomes an override in .env;
// an unset or blank one is no override at all, and nothing else is picked up.
func TestProviderOverridesComeFromTheEnvironment(t *testing.T) {
	env := map[string]string{
		"SONGSTRESS_ANTRA_API_KEY":    " antra-7f3k ",
		"SONGSTRESS_QOBUZ_APP_ID":     "",
		"SONGSTRESS_QOBUZ_APP_SECRET": "secret-7f3k",
		"LOG_LEVEL":                   "DEBUG",
	}
	got := providerOverrides(func(name string) string { return env[name] })
	want := map[string]string{"SONGSTRESS_ANTRA_API_KEY": "antra-7f3k", "SONGSTRESS_QOBUZ_APP_SECRET": "secret-7f3k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := providerOverrides(func(string) string { return "" }); got != nil {
		t.Fatalf("no overrides in the environment must give none: %v", got)
	}

	var out strings.Builder
	noteProviderOverrides(&out, want, false)
	if !strings.Contains(out.String(), "SONGSTRESS_ANTRA_API_KEY, SONGSTRESS_QOBUZ_APP_SECRET") {
		t.Errorf("the note should name the keys: %q", out.String())
	}
	for _, value := range want {
		if strings.Contains(out.String(), value) {
			t.Errorf("the note printed a key's value: %q", out.String())
		}
	}
}

// The whole --yes --dry-run path: a provider key in the environment is named,
// forwarded and written as an override, and its value is printed nowhere.
func TestDryRunTakesProviderOverridesFromTheEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("SONGSTRESS_ANTRA_API_KEY", "sct.antra.7f3k")
	f := flagsWith(t, func(f *installFlags) {
		f.DryRun, f.SongstressImage = true, "ghcr.io/pacholoamit/songstress:v0.32.0"
	})
	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	if err := runInstall(cmd, f); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"provider key overrides from your environment: SONGSTRESS_ANTRA_API_KEY",
		"- SONGSTRESS_ANTRA_API_KEY=${SONGSTRESS_ANTRA_API_KEY:-}",
		"SONGSTRESS_ANTRA_API_KEY='" + deploy.Hidden + "'",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
	if strings.Contains(out.String(), "sct.antra.7f3k") {
		t.Error("the dry run printed the key")
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
	err := ensureDir(t.TempDir(), "data", os.Getuid()+1, os.Getgid())
	if err == nil || !strings.Contains(err.Error(), "sudo chown") {
		t.Fatalf("expected an ownership error with the fix, got %v", err)
	}
}

// data/ belongs to the containers' user, who can swap data/navidrome for a
// symlink; a root re-run must refuse it, never chown (or create) through it.
func TestEnsureDirNeverFollowsASymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{filepath.Join("data", "navidrome"), "data2"} {
		if err := os.Symlink(outside, filepath.Join(root, planted)); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{filepath.Join("data", "navidrome"), filepath.Join("data2", "navidrome")} {
		err := ensureDir(root, rel, os.Getuid(), os.Getgid())
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("ensureDir(%s) = %v, want a refusal to follow the symlink", rel, err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("created %v through the symlink", entries)
	}
}

// A re-run keeps the image the stack runs unless this CLI pins a newer release:
// never a silent downgrade (the datastore schema would be ahead of the worker)
// and never a silent swap away from an override.
func TestSongstressImageOnRerun(t *testing.T) {
	const pin = "ghcr.io/pacholoamit/songstress:v0.32.0"
	cases := []struct {
		name, previous, override, want string
		noted                          bool
	}{
		{"fresh install", "", "", pin, false},
		{"same pin", pin, "", pin, false},
		{"upgrade to a newer pin", "ghcr.io/pacholoamit/songstress:v0.31.0", "", pin, true},
		{"never downgrade", "ghcr.io/pacholoamit/songstress:v0.33.1", "", "ghcr.io/pacholoamit/songstress:v0.33.1", true},
		{"keep an override", "songstress:e2e", "", "songstress:e2e", true},
		{"an override wins", "ghcr.io/pacholoamit/songstress:v0.31.0", "songstress:dev", "songstress:dev", false},
	}
	for _, c := range cases {
		got, note := songstressImage(pin, c.previous, c.override)
		if got != c.want || (note != "") != c.noted {
			t.Errorf("%s: got %q (note %q), want %q", c.name, got, note, c.want)
		}
	}
}
