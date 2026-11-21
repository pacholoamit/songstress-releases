package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// flagsWith is a valid --yes flag set rooted in temp dirs (--music-dir must exist).
func flagsWith(t *testing.T, mut func(*installFlags)) installFlags {
	t.Helper()
	f := installFlags{Yes: true, MusicDir: t.TempDir(), InstallDir: t.TempDir(), Port: 8090, VoicePort: deploy.DefaultVoicePort, TZ: "UTC"}
	if mut != nil {
		mut(&f)
	}
	return f
}

func TestComponentsFlagParsing(t *testing.T) {
	a, err := answersFromFlags(flagsWith(t, func(f *installFlags) {
		f.Components, f.WGPrivateKey = "vpn", "k"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !a.VPN {
		t.Fatalf("bad components: %+v", a)
	}
	// The Discovery side-car is retired: Songstress analyses its music itself.
	if _, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.Components = "discovery" })); err == nil ||
		!strings.Contains(err.Error(), "valid: vpn") {
		t.Fatalf("discovery must be refused, got %v", err)
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

// `songstress install --yes` on an existing install reproduces it: flags the
// operator didn't pass come from the lock, flags they did pass win.
func TestInheritAnswersFromTheLock(t *testing.T) {
	prev := deploy.Answers{MusicDir: "/srv/music", Port: 9000, TZ: "Asia/Manila", VPN: true,
		AdminEmail: "me@x.y", Telemetry: false, PublicURL: "https://music.example.com"}
	f := installFlags{Port: 8090, TZ: "UTC", AdminEmail: "admin@songstress.local", Telemetry: true, Components: "vpn"}
	changed := map[string]bool{"components": true}
	inheritAnswers(&f, prev, func(name string) bool { return changed[name] })
	if f.MusicDir != "/srv/music" || f.Port != 9000 || f.TZ != "Asia/Manila" || f.AdminEmail != "me@x.y" ||
		f.Telemetry || f.PublicURL != "https://music.example.com" {
		t.Fatalf("not inherited: %+v", f)
	}
	if f.Components != "vpn" {
		t.Fatalf("an explicit flag must win: %q", f.Components)
	}
}

// A re-run's own stack holds the port its .env names; only someone else's
// hold counts as "in use" — the port a separate music server once held is no
// longer the install's.
func TestPortInUseIgnoresTheInstallsOwnPort(t *testing.T) {
	prev := deploy.Existing{Env: map[string]string{"SONGSTRESS_PORT": "8090", "NAVIDROME_PORT": "4533"}}
	inUse := portInUse(prev, func(int) bool { return false }) // every port taken
	if inUse(8090) {
		t.Fatal("the install's own port is not in someone else's use")
	}
	if !inUse(9000) || !inUse(4533) {
		t.Fatal("a port the install doesn't hold is in use")
	}
	if !portInUse(deploy.Existing{}, func(int) bool { return false })(8090) {
		t.Fatal("a fresh install holds no port")
	}
}

// A taken port is refused before anything is written, with the flag that
// fixes it and a port that is free — not left to fail `docker compose up`.
func TestBusyPortsSayHowToFixThem(t *testing.T) {
	taken := map[int]bool{8090: true, 8091: true}
	inUse := func(p int) bool { return taken[p] }
	free := func(int) bool { return false }
	err := busyPorts(deploy.Answers{Port: 8090, VoicePort: 8093}, hostPorts{dashboard: inUse, voice: free})
	for _, w := range []string{"port 8090 is already in use", "--port (8092 is free)"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("%v lacks %q", err, w)
		}
	}
	if err := busyPorts(deploy.Answers{Port: 9000, VoicePort: 8093}, hostPorts{dashboard: inUse, voice: free}); err != nil {
		t.Errorf("a free port: %v", err)
	}
}

// Jam voice's port is refused the same way, and the free one offered instead
// suits voice: not the dashboard's port, nor one the container uses itself.
func TestBusyVoicePortSaysHowToFixIt(t *testing.T) {
	voiceTaken := map[int]bool{8089: true}
	a := deploy.Answers{Port: 8091, VoicePort: 8089}
	err := busyPorts(a, hostPorts{dashboard: func(int) bool { return false }, voice: func(p int) bool { return voiceTaken[p] }})
	for _, w := range []string{"Jam voice's port 8089 is already in use", "(TCP or UDP)", "--voice-port (8093 is free)"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("%v lacks %q", err, w)
		}
	}
}

// --voice-port is checked with the others: a port of its own, off the ports
// the container uses itself (gluetun's too, behind the VPN), in range.
func TestVoicePortFlag(t *testing.T) {
	cases := []struct {
		voice      int
		components string
		want       string
	}{
		{8093, "", ""},
		{8095, "vpn", ""},
		{8000, "", ""},
		{8090, "", "the dashboard's port"},
		{8092, "", "worker"},
		{8000, "vpn", "gluetun's control server"},
		{0, "", "from 1 to 65535"},
		{70000, "", "from 1 to 65535"},
	}
	for _, c := range cases {
		a, err := answersFromFlags(flagsWith(t, func(f *installFlags) {
			f.VoicePort, f.Components, f.WGPrivateKey = c.voice, c.components, "k"
		}))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("--voice-port %d (%s): %v", c.voice, c.components, err)
		case c.want == "" && a.VoicePort != c.voice:
			t.Errorf("--voice-port %d answered %d", c.voice, a.VoicePort)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("--voice-port %d (%s): %v, want %q", c.voice, c.components, err, c.want)
		}
	}
	if _, err := answersFromFlags(flagsWith(t, func(f *installFlags) { f.Port, f.VoicePort = 9000, 9000 })); err == nil ||
		!strings.Contains(err.Error(), "one of its own") {
		t.Errorf("voice on the dashboard's port: %v", err)
	}
}

// A re-run's own stack holds the voice port its .env names, over both TCP and
// UDP; anyone else's hold on either side takes the port.
func TestVoicePortInUseIgnoresTheInstallsOwnPort(t *testing.T) {
	prev := deploy.Existing{Env: map[string]string{"VOICE_PORT": "8093"}}
	none := func(int) bool { return false } // every port taken
	all := func(int) bool { return true }
	if voicePortInUse(prev, none, none)(8093) {
		t.Fatal("the install's own voice port is not in someone else's use")
	}
	if !voicePortInUse(prev, all, none)(8095) || !voicePortInUse(prev, none, all)(8095) {
		t.Fatal("a hold on either TCP or UDP takes the port")
	}
	if voicePortInUse(prev, all, all)(8095) {
		t.Fatal("a port free on both sides is free")
	}
	if !voicePortInUse(deploy.Existing{}, all, none)(8093) {
		t.Fatal("an install from before voice holds no voice port")
	}
}

// Without a terminal there's no wizard; say so instead of complaining about a
// --yes flag nobody passed.
func TestNoTerminalAsksForYes(t *testing.T) {
	_, _, err := collectAnswers(installFlags{InstallDir: t.TempDir()}, deploy.Existing{}, false, hostPorts{})
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
	a := deploy.Answers{InstallDir: dir, PUID: os.Getuid(), PGID: os.Getgid()}
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
	a := deploy.Answers{InstallDir: "/srv/songstress", MusicDir: "/srv/music", Port: 8090, VoicePort: 8093, AdminEmail: "me@x.y"}
	var out strings.Builder
	printSummary(&out, a, outcome{lan: "192.168.1.20"})
	for _, want := range []string{
		"open http://localhost:8090 (from another device: http://192.168.1.20:8090)",
		"Jam voice: UDP and TCP 8093 — let both through this host's firewall",
		"grep SONGSTRESS_ADMIN_PASSWORD= /srv/songstress/.env",
		"desktop & mobile apps: enter http://192.168.1.20:8090",
		"your library: /srv/music",
		"re-run `songstress install --install-dir /srv/songstress`",
		installURL,
		"--public-url",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "importing") {
		t.Error("a fresh install has nothing to import")
	}
	out.Reset()
	printSummary(&out, a, outcome{rerun: true, keptAdmin: true, defaultDir: true, imports: true})
	for _, want := range []string{"is updated and up", "your sign-in is unchanged (me@x.y", "re-run `songstress install` to",
		"importing from your previous music server"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("re-run summary lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "grep") {
		t.Error("a re-run has nothing new to show")
	}
	out.Reset()
	a.VPN = true
	printSummary(&out, a, outcome{})
	if !strings.Contains(out.String(), "Jam voice: UDP and TCP 8093, published on gluetun") {
		t.Errorf("a VPN install's summary says where voice is published:\n%s", out.String())
	}
}

// Before a re-run replaces the install's files, the ones it ran are kept in
// previous/ — what rolling back to that release needs — the .env (passwords)
// for the operator alone, and nothing left there from an earlier run.
func TestKeepPreviousKeepsTheFilesARerunReplaces(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"compose.yaml": "old compose\n", ".env": "OLD=1\n",
		deploy.LockName: "{}\n", "compose.vpn.yaml": "old vpn\n", "notes.txt": "mine\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(dir, previousDir, "compose.discovery.yaml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("from an earlier run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := keepPrevious(dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"compose.yaml": "old compose\n", ".env": "OLD=1\n", deploy.LockName: "{}\n", "compose.vpn.yaml": "old vpn\n"} {
		b, err := os.ReadFile(filepath.Join(dir, previousDir, name))
		if err != nil || string(b) != want {
			t.Errorf("previous/%s = %q (%v), want %q", name, b, err, want)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an overlay this install no longer has must not linger in previous/")
	}
	if _, err := os.Stat(filepath.Join(dir, previousDir, "notes.txt")); !os.IsNotExist(err) {
		t.Error("only the installer's own files are kept")
	}
	for _, name := range []string{"", ".env"} {
		if st, err := os.Stat(filepath.Join(dir, previousDir, name)); err != nil || st.Mode().Perm()&0o077 != 0 {
			t.Errorf("previous/%s must be the operator's alone: %v %v", name, st.Mode().Perm(), err)
		}
	}
	link := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(link, previousDir)); err != nil {
		t.Fatal(err)
	}
	if err := keepPrevious(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("a planted symlink must be refused, got %v", err)
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

// An install that ran the Discovery side-car hears that it goes, with the
// volumes to delete, named after the install directory as compose names them.
func TestSideCarNoteNamesTheVolumesItLeaves(t *testing.T) {
	prev := deploy.Existing{Env: map[string]string{"AUDIOMUSE_DB_PASSWORD": "d"}}
	n := sideCarNote("/srv/Songstress", prev)
	if !strings.Contains(n, "docker volume rm songstress_audiomuse-pgdata songstress_audiomuse-redisdata") {
		t.Fatalf("got %q", n)
	}
	if sideCarNote("/srv/songstress", deploy.Existing{Env: map[string]string{}}) != "" {
		t.Fatal("an install that never ran it hears nothing")
	}
}

// An install from before keeps the database of the music server it ran beside
// in data/navidrome, which its first start imports from: a file, found on disk
// at every run. Anything else there is nothing to import.
func TestImportDataIsTheDatabaseFileAnInstallKept(t *testing.T) {
	dir := t.TempDir()
	if importData(dir) {
		t.Fatal("a fresh install has nothing to import")
	}
	db := filepath.Join(dir, "data", "navidrome", "navidrome.db")
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}
	if importData(dir) {
		t.Fatal("a directory named navidrome.db is no database")
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("its library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !importData(dir) {
		t.Fatal("data/navidrome/navidrome.db is the database to import")
	}
	a, _, err := collectAnswers(flagsWith(t, func(f *installFlags) { f.InstallDir = dir }), deploy.Existing{}, false, hostPorts{})
	if err != nil || !a.ImportData {
		t.Fatalf("the answers must carry it: %v, %+v", err, a)
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
		PUID: 1000, PGID: 1000, AdminEmail: "a@b.c", VPN: true, SMTPHost: "smtp.x.y", SMTPPort: 587}
	secrets := []string{"admin-7f3k", "wg-7f3k", "smtp-7f3k", "own-7f3k"}
	s := deploy.Secrets{AdminPassword: secrets[0], WGPrivateKey: secrets[1], SMTPPassword: secrets[2],
		Extra: map[string]string{"GLUETUN_API_KEY": secrets[3]}}
	var out strings.Builder
	if err := printDryRun(&out, a, s, m); err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the dry run printed %s", secret)
		}
	}
	for _, want := range []string{"compose.yaml", "compose.vpn.yaml", "SONGSTRESS_ADMIN_PASSWORD='" + deploy.Hidden + "'", "GLUETUN_API_KEY=",
		"VOICE_PORT=8093\n", "${VOICE_PORT:-8093}:${VOICE_PORT:-8093}/udp"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
}

// A provider key in the installer's environment becomes an override in .env;
// an unset or blank one is no override at all, and nothing else is picked up.
func TestProviderOverridesComeFromTheEnvironment(t *testing.T) {
	env := map[string]string{
		"SONGSTRESS_COMMUNITY_API_KEY": " community-7f3k ",
		"SONGSTRESS_QOBUZ_APP_ID":      "",
		"SONGSTRESS_QOBUZ_APP_SECRET":  "secret-7f3k",
		"LOG_LEVEL":                    "DEBUG",
	}
	got := providerOverrides(func(name string) string { return env[name] })
	want := map[string]string{"SONGSTRESS_COMMUNITY_API_KEY": "community-7f3k", "SONGSTRESS_QOBUZ_APP_SECRET": "secret-7f3k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := providerOverrides(func(string) string { return "" }); got != nil {
		t.Fatalf("no overrides in the environment must give none: %v", got)
	}

	var out strings.Builder
	noteProviderOverrides(&out, want)
	if !strings.Contains(out.String(), "SONGSTRESS_COMMUNITY_API_KEY, SONGSTRESS_QOBUZ_APP_SECRET") {
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
	t.Setenv("SONGSTRESS_COMMUNITY_API_KEY", "sct.community.7f3k")
	f := flagsWith(t, func(f *installFlags) {
		f.DryRun, f.SongstressImage = true, "ghcr.io/pacholoamit/songstress:v1.0.0"
	})
	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	if err := runInstall(cmd, f); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"provider key overrides from your environment: SONGSTRESS_COMMUNITY_API_KEY",
		"- SONGSTRESS_COMMUNITY_API_KEY=${SONGSTRESS_COMMUNITY_API_KEY:-}",
		"SONGSTRESS_COMMUNITY_API_KEY='" + deploy.Hidden + "'",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
	if strings.Contains(out.String(), "sct.community.7f3k") {
		t.Error("the dry run printed the key")
	}
}

// The datastore directory exists, owned by the containers' user, before the
// first `up`; a .env left world-readable by hand comes back 0600; and an
// install's data/navidrome, which the first start imports from, is left as it
// was.
func TestWriteDeploymentPreparesTheDatastore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := deploy.Answers{InstallDir: dir, PUID: os.Getuid(), PGID: os.Getgid()}
	r := deploy.Rendered{Files: map[string][]byte{".env": []byte("A=1\n"), "compose.yaml": []byte("services: {}\n")}}
	if err := writeDeployment(a, r); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dir, "data")); err != nil || !st.IsDir() {
		t.Fatalf("data/ must exist before the first `up`: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "navidrome")); !os.IsNotExist(err) {
		t.Fatal("a fresh install has no data/navidrome")
	}
	if st, _ := os.Stat(filepath.Join(dir, ".env")); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v", st.Mode().Perm())
	}

	before := t.TempDir()
	db := filepath.Join(before, "data", "navidrome", "navidrome.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("its library"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.InstallDir = before
	if err := writeDeployment(a, r); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(db); err != nil || string(b) != "its library" {
		t.Fatalf("data/navidrome/navidrome.db = %q, %v", b, err)
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

// data/ belongs to the containers' user, who can swap a directory in it for a
// symlink; a root re-run must refuse it, never chown (or create) through it.
func TestEnsureDirNeverFollowsASymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{filepath.Join("data", "cache"), "data2"} {
		if err := os.Symlink(outside, filepath.Join(root, planted)); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{filepath.Join("data", "cache"), filepath.Join("data2", "cache")} {
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
