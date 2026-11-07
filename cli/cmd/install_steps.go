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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
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
		f.Components = ""
		if prev.VPN {
			f.Components = "vpn"
		}
	})
	keep("admin-email", func() {
		if prev.AdminEmail != "" {
			f.AdminEmail = prev.AdminEmail
		}
	})
	keep("skip-admin-seed", func() { f.SkipAdminSeed = prev.SkipAdminSeed })
	keep("telemetry", func() { f.Telemetry = prev.Telemetry })
	keep("public-url", func() { f.PublicURL = prev.PublicURL })
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
// holds: a re-run's own stack already holds the port its .env names.
func portInUse(prev deploy.Existing, free func(int) bool) func(int) bool {
	own, err := strconv.Atoi(prev.Env["SONGSTRESS_PORT"])
	return func(p int) bool { return (err != nil || p != own) && !free(p) }
}

// busyPorts refuses the host port this run publishes when something else
// already holds it — before anything is written. Left to `docker compose up`,
// it failed after every file was written, with Docker's own words for it.
func busyPorts(a deploy.Answers, inUse func(int) bool) error {
	if !inUse(a.Port) {
		return nil
	}
	return fmt.Errorf("port %d is already in use on this host — choose another with --port (%d is free)",
		a.Port, nextFree(a.Port, inUse))
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
func collectAnswers(f installFlags, prev deploy.Existing, prompt bool, inUse func(int) bool) (deploy.Answers, deploy.Secrets, error) {
	var a deploy.Answers
	var s deploy.Secrets
	var err error
	if !prompt {
		if !f.Yes && f.MusicDir == "" {
			return a, s, errors.New("there's no terminal here for the setup wizard — run `songstress install` in a terminal, " +
				"or pass --yes with --music-dir (`songstress install --help` lists every flag)")
		}
		if a, err = answersFromFlags(f); err != nil {
			return a, s, err
		}
		s.WGPrivateKey = f.WGPrivateKey
		s.AdminPassword = f.AdminPassword // set ⇒ "choose"; empty ⇒ generated (or kept)
	} else {
		defaults := deploy.Answers{
			InstallDir: f.InstallDir, MusicDir: f.MusicDir, Port: f.Port, TZ: f.TZ,
			PUID: os.Getuid(), PGID: os.Getgid(), Telemetry: true, AdminEmail: f.AdminEmail,
			SkipAdminSeed: f.SkipAdminSeed, PublicURL: f.PublicURL, Keep: prev.Keep(),
		}
		if prev.HasLock {
			defaults = withPrevious(defaults, prev.Lock.Answers)
		}
		opts := wizard.Options{Rerun: prev.Found(), PortInUse: inUse}
		if a, s, err = wizard.Run(defaults, opts); err != nil {
			return a, s, err
		}
	}
	if !prev.Found() {
		a.PUID, a.PGID = fileOwner(a.PUID, a.PGID, a.MusicDir)
	}
	carryOver(&a, prev, !prompt)
	a.ImportData = importData(a.InstallDir)
	return a, s, nil
}

// sideCarNote tells an install that ran the Discovery side-car (AudioMuse-AI)
// that it leaves the stack, and how to delete the volumes it leaves behind
// ("" for any other install). Compose names them after the install directory.
func sideCarNote(dir string, prev deploy.Existing) string {
	if prev.Env["AUDIOMUSE_DB_PASSWORD"] == "" {
		return ""
	}
	project := strings.ToLower(filepath.Base(dir))
	return "Discovery (AudioMuse-AI) leaves the stack too: Songstress analyses your music's sound itself. " +
		"Its database volumes stay until you delete them: docker volume rm " +
		project + "_audiomuse-pgdata " + project + "_audiomuse-redisdata"
}

// importData reports an install that keeps the database of the music server
// it ran beside until Songstress became one: the bundled one kept it in
// data/navidrome. The first start imports from it, so the compose mounts it.
func importData(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "data", "navidrome", "navidrome.db"))
	return err == nil && info.Mode().IsRegular()
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
func noteProviderOverrides(out io.Writer, keys map[string]string) {
	var names []string
	for _, name := range deploy.ProviderKeys {
		if _, ok := keys[name]; ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	fmt.Fprintln(out, ui.Styles.Dim.Render("• provider key overrides from your environment: "+strings.Join(names, ", ")))
}

// withPrevious fills wizard defaults from the lock.
func withPrevious(d, prev deploy.Answers) deploy.Answers {
	d.MusicDir = firstNonEmpty(d.MusicDir, prev.MusicDir)
	d.VPN, d.Telemetry = prev.VPN, prev.Telemetry
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
// earlier run whose component is now off, or that this release no longer
// makes (the Discovery side-car's), is removed with it.
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

// previousDir, in the install directory, holds the files the last re-run
// that changed something replaced, as they were: what rolling back to the
// release they ran needs (README "Rolling back").
const previousDir = "previous"

// keepPrevious copies the install's generated files and its lock into
// previousDir before a re-run replaces them. The .env holds the install's
// passwords, so the directory is the operator's alone. A file that isn't there
// now is removed from it, so it never mixes two installs' files.
func keepPrevious(dir string) error {
	target := filepath.Join(dir, previousDir)
	if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := os.Lstat(target); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is a symlink or a file, not a directory; the installer won't follow it — remove it and re-run", target)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		return err
	}
	for _, name := range append([]string{"compose.yaml", ".env", deploy.LockName}, overlays...) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Remove(filepath.Join(target, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
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

// writeDeployment writes the generated files and creates data/, the
// datastore the container mounts, owned by PUID:PGID: one Docker creates on a
// Linux host is root-owned.
func writeDeployment(a deploy.Answers, r deploy.Rendered) error {
	if err := ensureDir(a.InstallDir, "data", a.PUID, a.PGID); err != nil {
		return err
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

// ensureDir creates rel under root and makes sure uid:gid owns it. Without root
// a chown to another user fails; then an existing directory must already be
// theirs.
//
// Nothing under root is followed: data/ belongs to PUID:PGID and is the
// songstress container's datastore, so that user, or a process in the
// container, can swap a directory in it for a symlink. Run as root, a chown
// through one would hand its target (/etc, say) to PUID:PGID. So each part of
// rel must be a real directory, and the chown is an Lchown of the last.
func ensureDir(root, rel string, uid, gid int) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	dir := root
	var info fs.FileInfo
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		var err error
		if info, err = os.Lstat(dir); err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is a symlink or a file, not a directory; the installer won't follow it — remove it and re-run", dir)
		}
	}
	if err := os.Lchown(dir, uid, gid); err == nil || !errors.Is(err, fs.ErrPermission) {
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
