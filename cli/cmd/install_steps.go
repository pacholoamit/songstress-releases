package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/navidrome"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
	"github.com/pacholoamit/songstress-releases/cli/internal/ui"
	"github.com/pacholoamit/songstress-releases/cli/internal/wizard"
)

// resolveDirs makes the install and music directories absolute and, when ask
// is set (the wizard, with no --install-dir), asks for the install directory
// before anything else: an install already there decides every other default
// and keeps its passwords. Asked later, a directory holding an install was
// treated as empty, and its passwords were replaced.
func resolveDirs(f *installFlags, ask bool) error {
	if ask {
		dir, err := wizard.AskInstallDir(f.InstallDir)
		if err != nil {
			return err
		}
		f.InstallDir = dir
	}
	var err error
	if f.InstallDir, err = deploy.AbsPath(f.InstallDir); err != nil {
		return err
	}
	f.MusicDir, err = deploy.AbsPath(f.MusicDir)
	return err
}

// inheritAnswers makes a re-run reproduce the existing install: every flag the
// operator didn't pass takes the value recorded in the lock, so
// `songstress install --yes` alone regenerates the same deployment (new pins,
// same mode, same answers).
func inheritAnswers(f *installFlags, prev deploy.Answers, changed func(string) bool) {
	keep := func(flag string, apply func()) {
		if !changed(flag) {
			apply()
		}
	}
	keep("music-dir", func() { f.MusicDir = prev.MusicDir })
	keep("port", func() {
		if prev.Port != 0 {
			f.Port = prev.Port
		}
	})
	keep("tz", func() {
		if prev.TZ != "" {
			f.TZ = prev.TZ
		}
	})
	keep("components", func() {
		var c []string
		if prev.Discovery {
			c = append(c, "discovery")
		}
		if prev.VPN {
			c = append(c, "vpn")
		}
		f.Components = strings.Join(c, ",")
	})
	keep("admin-email", func() {
		if prev.AdminEmail != "" {
			f.AdminEmail = prev.AdminEmail
		}
	})
	keep("skip-admin-seed", func() { f.SkipAdminSeed = prev.SkipAdminSeed })
	keep("telemetry", func() { f.Telemetry = prev.Telemetry })
	keep("public-url", func() { f.PublicURL = prev.PublicURL })
	keep("navidrome-url", func() { f.NavidromeURL = prev.NavidromeURL })
	keep("navidrome-username", func() { f.NavidromeUsername = prev.NavidromeUsername })
	keep("navidrome-public-url", func() { f.NavidromePublicURL = prev.NavidromePublicURL })
	keep("publish-navidrome", func() { f.PublishNavidrome = !prev.NoNavidromePort })
}

// songstressImage picks the image a run installs. An override always wins. On a
// re-run the previous install's image is kept unless this CLI pins a NEWER
// release — that's the upgrade path. It is never silently swapped for an older
// release (the datastore's schema would be ahead of the worker, which refuses
// to start) or for the pin when the previous image was an override.
func songstressImage(pinned, previous, override string) (image, note string) {
	switch {
	case override != "":
		return override, ""
	case previous == "" || previous == pinned:
		return pinned, ""
	case deploy.OlderRelease(previous, pinned):
		return pinned, "updating songstress: " + previous + " → " + pinned
	}
	return previous, "keeping " + previous + " from the previous install (this CLI pins " + pinned +
		"; pass --songstress-image to change it)"
}

// portInUse reports a host port that something other than this install
// holds: a re-run's own stack already holds the ports its .env names.
func portInUse(prev deploy.Existing, free func(int) bool) func(int) bool {
	own := map[int]bool{}
	for _, key := range []string{"SONGSTRESS_PORT", "NAVIDROME_PORT"} {
		if p, err := strconv.Atoi(prev.Env[key]); err == nil {
			own[p] = true
		}
	}
	return func(p int) bool { return !own[p] && !free(p) }
}

// navidromePort is the bundled Navidrome's host port: the operator's choice
// (--navidrome-port, the wizard, or NAVIDROME_PORT in .env), else 4533.
func navidromePort(a deploy.Answers) string {
	return firstNonEmpty(a.Keep["NAVIDROME_PORT"], wizard.DefaultNavidromePort)
}

// busyPorts refuses a host port this run publishes that something else
// already holds — before anything is written. Left to `docker compose up`, it
// failed after every file was written, with Docker's own words for it.
func busyPorts(a deploy.Answers, legacy bool, inUse func(int) bool) error {
	var problems []string
	if inUse(a.Port) {
		problems = append(problems, fmt.Sprintf("port %d is already in use on this host — choose another with --port (%d is free)",
			a.Port, nextFree(a.Port, inUse)))
	}
	if a.NavidromePublished(legacy) {
		// A NAVIDROME_PORT hand-edited to "127.0.0.1:4533" is the operator's own.
		if p, err := strconv.Atoi(navidromePort(a)); err == nil && inUse(p) {
			fix := fmt.Sprintf("choose another with --navidrome-port (%d is free)", nextFree(p, inUse))
			if !legacy {
				fix += ", or keep it closed with --publish-navidrome=false (Songstress doesn't need it)"
			}
			problems = append(problems, fmt.Sprintf("port %d, for the bundled Navidrome, is already in use on this host — %s", p, fix))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "\n  "))
}

// nextFree is the first port after p that nothing holds.
func nextFree(p int, inUse func(int) bool) int {
	for q := p + 1; q < 65536 && q < p+200; q++ {
		if !inUse(q) {
			return q
		}
	}
	return p + 1
}

// collectAnswers runs the wizard or reads the flags, then applies what a
// re-run must carry over from the existing install.
func collectAnswers(f installFlags, pre preflight.Result, prev deploy.Existing, legacy, prompt bool, inUse func(int) bool) (deploy.Answers, deploy.Secrets, error) {
	var a deploy.Answers
	var s deploy.Secrets
	var err error
	keep := map[string]string{}
	if f.NavidromePortSet {
		if f.NavidromePort < 1 || f.NavidromePort > 65535 {
			return a, s, fmt.Errorf("--navidrome-port %d: use a port from 1 to 65535", f.NavidromePort)
		}
		keep["NAVIDROME_PORT"] = strconv.Itoa(f.NavidromePort)
	}
	if !prompt {
		if !f.Yes && f.MusicDir == "" {
			return a, s, errors.New("there's no terminal here for the setup wizard — run `songstress install` in a terminal, " +
				"or pass --yes with --music-dir (`songstress install --help` lists every flag)")
		}
		if a, err = answersFromFlags(f); err != nil {
			return a, s, err
		}
		a.Keep = keep
		s.WGPrivateKey = f.WGPrivateKey
		s.AdminPassword = f.AdminPassword // set ⇒ "choose"; empty ⇒ generated (or kept)
		s.NavidromePassword = f.NavidromePassword
	} else {
		defaults := deploy.Answers{
			InstallDir: f.InstallDir, MusicDir: f.MusicDir, Port: f.Port, TZ: f.TZ,
			PUID: os.Getuid(), PGID: os.Getgid(), Telemetry: true, AdminEmail: f.AdminEmail,
			SkipAdminSeed: f.SkipAdminSeed, PublicURL: f.PublicURL,
			NavidromeUsername: f.NavidromeUsername, NavidromePublicURL: f.NavidromePublicURL,
			NavidromeURL: f.NavidromeURL, NoNavidromePort: !f.PublishNavidrome,
			Keep: mergeKeep(prev.Keep(), keep),
		}
		if prev.HasLock {
			defaults = withPrevious(defaults, prev.Lock.Answers)
		}
		opts := wizard.Options{Rerun: prev.Found(), LegacyMedia: legacy, PortInUse: inUse,
			KeepNavidromePassword: prev.Env["NAVIDROME_URL"] != "" && prev.Env["NAVIDROME_PASSWORD"] != ""}
		if a, s, err = wizard.Run(pre, defaults, opts); err != nil {
			return a, s, err
		}
		if a.NavidromeURL != "" {
			if a.NavidromeURL, a.NavidromeHostGateway, err = containerNavidromeURL(a.NavidromeURL); err != nil {
				return a, s, err
			}
		}
	}
	if a.Discovery && !pre.Host.HasAVX2 {
		a.NoAVX2 = true
	}
	if !prev.Found() {
		a.PUID, a.PGID = fileOwner(a.PUID, a.PGID, a.MusicDir)
	}
	carryOver(&a, prev, !prompt)
	if !a.ManagedNavidrome() && legacy && a.NavidromePublicURL == "" {
		a.NavidromePublicURL = hostView(a.NavidromeURL, a.NavidromeHostGateway)
	}
	return a, s, nil
}

// mergeKeep is base with every value in over laid on it.
func mergeKeep(base, over map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, over)
	return out
}

// providerOverrides are the acquisition-provider keys set in the installer's
// own environment (deploy.ProviderKeys, the names the server reads). The image
// carries working values, so there is nothing to ask for: a key given this way
// is written to .env as an override, an unset one is left alone, and one
// already in .env is kept.
func providerOverrides(getenv func(string) string) map[string]string {
	var out map[string]string
	for _, name := range deploy.ProviderKeys {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[name] = value
		}
	}
	return out
}

// noteProviderOverrides names (never shows) the keys taken from the
// environment, so the operator knows they landed.
func noteProviderOverrides(out io.Writer, keys map[string]string, legacy bool) {
	var names []string
	for _, name := range deploy.ProviderKeys {
		if _, ok := keys[name]; ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	line := "• provider key overrides from your environment: " + strings.Join(names, ", ")
	if legacy {
		line += " (kept in .env; servers read them from v0.32.0)"
	}
	fmt.Fprintln(out, ui.Styles.Dim.Render(line))
}

// withPrevious fills wizard defaults from the lock.
func withPrevious(d, prev deploy.Answers) deploy.Answers {
	d.MusicDir = firstNonEmpty(d.MusicDir, prev.MusicDir)
	d.Discovery, d.VPN, d.Telemetry = prev.Discovery, prev.VPN, prev.Telemetry
	d.SMTPHost, d.SMTPPort, d.SMTPUsername = prev.SMTPHost, prev.SMTPPort, prev.SMTPUsername
	d.SMTPFrom, d.SMTPTo, d.SMTPStartTLS = prev.SMTPFrom, prev.SMTPTo, prev.SMTPStartTLS
	return d
}

// carryOver applies what a re-run keeps no matter how the answers were
// collected: the file owner, the admin mode, hand-edited .env values (under
// whatever this run chose), and — with --yes, which has no SMTP flags — the
// SMTP settings.
func carryOver(a *deploy.Answers, prev deploy.Existing, scripted bool) {
	if !prev.Found() {
		return
	}
	p := prev.Lock.Answers
	if prev.HasLock && p.PUID != 0 {
		a.PUID, a.PGID = p.PUID, p.PGID
	}
	if prev.HasLock {
		// The admin either was seeded or is created in-app; that can't change now.
		a.SkipAdminSeed = p.SkipAdminSeed
		a.AdminEmail = firstNonEmpty(p.AdminEmail, a.AdminEmail)
	}
	if scripted && a.SMTPHost == "" {
		a.SMTPHost, a.SMTPPort, a.SMTPUsername = p.SMTPHost, p.SMTPPort, p.SMTPUsername
		a.SMTPFrom, a.SMTPTo, a.SMTPStartTLS = p.SMTPFrom, p.SMTPTo, p.SMTPStartTLS
	}
	a.PublicURL = firstNonEmpty(a.PublicURL, prev.Env["SONGSTRESS_PUBLIC_URL"])
	a.Keep = mergeKeep(prev.Keep(), a.Keep)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// containerNavidromeURL turns the URL the operator typed into the one the
// songstress CONTAINER can use: localhost there is the container itself, so a
// Navidrome on this host is reached as host.docker.internal (mapped to the
// host gateway in compose — Docker Desktop has it built in, Linux needs it).
func containerNavidromeURL(raw string) (string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false, fmt.Errorf("--navidrome-url %q: enter a full URL such as http://192.168.1.10:4533", raw)
	}
	gateway := false
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		port := u.Port()
		u.Host = "host.docker.internal"
		if port != "" {
			u.Host += ":" + port
		}
		gateway = true
	case "host.docker.internal":
		gateway = true
	}
	return strings.TrimRight(u.String(), "/"), gateway, nil
}

// hostView is the URL as THIS host reaches it (host.docker.internal → 127.0.0.1).
func hostView(containerURL string, gateway bool) string {
	if !gateway {
		return containerURL
	}
	return strings.Replace(containerURL, "host.docker.internal", "127.0.0.1", 1)
}

// checkExistingNavidrome verifies the operator's Navidrome BEFORE anything is
// written: it answers, the login signs in, and the account is an admin. In the
// wizard a failed check asks again for the address and login, instead of
// throwing every answer away.
func checkExistingNavidrome(ctx context.Context, out io.Writer, a *deploy.Answers, s *deploy.Secrets, dryRun, prompt bool) error {
	if dryRun {
		fmt.Fprintln(out, ui.Styles.Dim.Render("• dry run: the Navidrome login at "+hostView(a.NavidromeURL, a.NavidromeHostGateway)+" was not checked"))
		return nil
	}
	for {
		problem := navidromeProblem(ctx, out, *a, *s)
		if problem == "" {
			return nil
		}
		if !prompt {
			return errors.New("not installing: " + problem)
		}
		fmt.Fprintln(out, ui.Styles.Warn.Render("• "+problem))
		raw, user, password := hostView(a.NavidromeURL, a.NavidromeHostGateway), a.NavidromeUsername, s.NavidromePassword
		derived := a.NavidromePublicURL == raw // the device address followed the URL
		if err := wizard.AskNavidrome(problem, &raw, &user, &password); err != nil {
			return err
		}
		u, gw, err := containerNavidromeURL(raw)
		if err != nil {
			return err
		}
		a.NavidromeURL, a.NavidromeHostGateway, a.NavidromeUsername = u, gw, strings.TrimSpace(user)
		s.NavidromePassword = password
		if derived {
			a.NavidromePublicURL = hostView(u, gw)
		}
	}
}

// navidromeProblem checks the login once: "" when it works, else what's wrong
// and what to try.
func navidromeProblem(ctx context.Context, out io.Writer, a deploy.Answers, s deploy.Secrets) string {
	if s.NavidromePassword == "" {
		return "an existing Navidrome needs its password: --navidrome-password or SONGSTRESS_NAVIDROME_PASSWORD"
	}
	target := hostView(a.NavidromeURL, a.NavidromeHostGateway)
	fmt.Fprintln(out, ui.Styles.Dim.Render("• Checking your Navidrome at "+target+"…"))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := navidrome.Check(ctx, nil, target, a.NavidromeUsername, s.NavidromePassword)
	if err == nil {
		return ""
	}
	hint := ""
	switch navidrome.Reason(err) {
	case navidrome.Unreachable:
		hint = " — check the URL and that Navidrome is running (the installer checks it from this machine)"
	case navidrome.Auth:
		hint = " — check the username and password"
	case navidrome.NotAdmin:
		hint = " — use an admin account (Navidrome → Users), or make this one an admin"
	}
	return err.Error() + hint
}

// fileOwner is who the containers run as on a fresh install: this user, or —
// run as root, the way a NAS needs `sudo` for Docker — the music library's
// owner, so what Songstress writes into the library isn't root's.
func fileOwner(uid, gid int, musicDir string) (int, int) {
	if uid != 0 {
		return uid, gid
	}
	info, err := os.Stat(musicDir)
	if err != nil {
		return uid, gid
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
		return int(st.Uid), int(st.Gid)
	}
	return uid, gid
}

// musicDirNote warns when this user can't write to the music library: the
// containers run as this user, and downloads and upgrades write there.
func musicDirNote(dir string) string {
	if dir == "" || syscall.Access(dir, 2 /* W_OK */) == nil {
		return ""
	}
	return fmt.Sprintf("%s isn't writable by uid %d: Songstress can play it, but downloads and upgrades into it will fail until it is",
		dir, os.Getuid())
}

// overlays are the generated compose files a component adds; one left from an
// earlier run whose component is now off is removed with it.
var overlays = []string{"compose.discovery.yaml", "compose.vpn.yaml"}

const generatedHeader = "# Generated by `songstress install`"

// changedFiles names the generated files whose content differs from what the
// install directory holds, and the overlays this run removes.
func changedFiles(dir string, files map[string][]byte) []string {
	var names []string
	for name, content := range files {
		if old, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(old, content) {
			names = append(names, name)
		}
	}
	for _, name := range staleOverlays(dir, files) {
		names = append(names, name+" (removed)")
	}
	sort.Strings(names)
	return names
}

// describeChanges is the re-run's report of what it rewrites.
func describeChanges(names []string) string {
	if len(names) == 0 {
		return "your compose files and .env are unchanged"
	}
	return "rewriting " + strings.Join(names, ", ")
}

// staleOverlays are generated overlays in dir that this run doesn't generate.
func staleOverlays(dir string, files map[string][]byte) []string {
	var stale []string
	for _, name := range overlays {
		if _, ok := files[name]; ok {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && bytes.HasPrefix(b, []byte(generatedHeader)) {
			stale = append(stale, name)
		}
	}
	return stale
}

// writeDeployment writes the generated files and creates the directories the
// containers mount, owned by PUID:PGID. Navidrome runs AS PUID:PGID; a
// data/navidrome that Docker creates on a Linux host is root-owned, Navidrome
// can't write its database there, never turns healthy, and the stack never
// comes up — so it is created here, before anything starts.
func writeDeployment(a deploy.Answers, r deploy.Rendered) error {
	dirs := []string{filepath.Join(a.InstallDir, "data")}
	if a.ManagedNavidrome() {
		dirs = append(dirs, filepath.Join(a.InstallDir, "data", "navidrome"))
	}
	for _, dir := range dirs {
		if err := ensureDir(dir, a.PUID, a.PGID); err != nil {
			return err
		}
	}
	for _, name := range staleOverlays(a.InstallDir, r.Files) {
		if err := os.Remove(filepath.Join(a.InstallDir, name)); err != nil {
			return err
		}
	}
	for name, content := range r.Files {
		mode := os.FileMode(0o644)
		if name == ".env" {
			mode = 0o600
		}
		path := filepath.Join(a.InstallDir, name)
		if err := os.WriteFile(path, content, mode); err != nil {
			return err
		}
		if err := os.Chmod(path, mode); err != nil { // WriteFile keeps an existing file's mode
			return err
		}
	}
	return nil
}

// ensureDir creates dir and makes sure uid:gid owns it. Without root a chown to
// another user fails; then an existing directory must already be theirs.
func ensureDir(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.Chown(dir, uid, gid); err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && (int(st.Uid) != uid || int(st.Gid) != gid) {
		return fmt.Errorf("%s is owned by %d:%d but the containers run as %d:%d — fix with: sudo chown %d:%d %s",
			dir, st.Uid, st.Gid, uid, gid, uid, gid, dir)
	}
	return nil
}

// superuserLogin is the datastore superuser the verification signs in as: the
// seeded admin pair, or — when the admin is created in-app — the pair the
// container generated into data/.admin-credentials on its first start.
func superuserLogin(ctx context.Context, a deploy.Answers, s deploy.Secrets) (string, string, error) {
	if !a.SkipAdminSeed {
		return a.AdminEmail, s.AdminPassword, nil
	}
	path := filepath.Join(a.InstallDir, "data", ".admin-credentials")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if b, err := os.ReadFile(path); err == nil {
			env := deploy.ParseEnv(b)
			return env["SONGSTRESS_ADMIN_EMAIL"], env["SONGSTRESS_ADMIN_PASSWORD"], nil
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the server never wrote %s", path)
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// lanAddress is this host's address on its network — what another device
// types — or "" when there's none. Dialing UDP sends nothing: it only asks the
// kernel which interface the default route leaves from.
func lanAddress() string {
	conn, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1: never answered
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
		return ""
	}
	return addr.IP.String()
}
