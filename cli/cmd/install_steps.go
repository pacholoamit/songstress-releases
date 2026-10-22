package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/navidrome"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
	"github.com/pacholoamit/songstress-releases/cli/internal/ui"
	"github.com/pacholoamit/songstress-releases/cli/internal/wizard"
)

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
}

// portsToCheck: a re-run's ports belong to its own running stack, so only a
// fresh install probes them. Navidrome's port is published only for servers
// that still redirect streams to it.
func portsToCheck(f installFlags, legacy, rerun bool) []int {
	if rerun {
		return nil
	}
	ports := []int{f.Port}
	if legacy && f.NavidromeURL == "" {
		ports = append(ports, 4533)
	}
	return ports
}

// collectAnswers runs the wizard or reads the flags, then applies what a
// re-run must carry over from the existing install.
func collectAnswers(f installFlags, pre preflight.Result, prev deploy.Existing, legacy bool) (deploy.Answers, deploy.Secrets, error) {
	var a deploy.Answers
	var s deploy.Secrets
	var err error
	if f.Yes || !ui.IsInteractive() {
		if a, err = answersFromFlags(f); err != nil {
			return a, s, err
		}
		s.WGPrivateKey = f.WGPrivateKey
		s.AdminPassword = f.AdminPassword // set ⇒ "choose"; empty ⇒ generated (or kept)
		s.NavidromePassword = f.NavidromePassword
	} else {
		defaults := deploy.Answers{
			InstallDir: f.InstallDir, MusicDir: f.MusicDir, Port: f.Port, TZ: f.TZ,
			PUID: os.Getuid(), PGID: os.Getgid(), Telemetry: true, AdminEmail: f.AdminEmail,
			SkipAdminSeed: f.SkipAdminSeed, PublicURL: f.PublicURL,
			NavidromeUsername: f.NavidromeUsername, NavidromePublicURL: f.NavidromePublicURL,
			NavidromeURL: f.NavidromeURL,
		}
		if prev.HasLock {
			defaults = withPrevious(defaults, prev.Lock.Answers)
		}
		opts := wizard.Options{Rerun: prev.Found(), LegacyMedia: legacy}
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
	carryOver(&a, prev, f.Yes || !ui.IsInteractive())
	if !a.ManagedNavidrome() && legacy && a.NavidromePublicURL == "" {
		a.NavidromePublicURL = hostView(a.NavidromeURL, a.NavidromeHostGateway)
	}
	return a, s, nil
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
// collected: the file owner, the admin mode, hand-edited .env values, and —
// with --yes, which has no SMTP flags — the SMTP settings.
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
	a.Keep = prev.Keep()
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
// written: it answers, the login signs in, and the account is an admin.
func checkExistingNavidrome(ctx context.Context, out io.Writer, a deploy.Answers, s deploy.Secrets, dryRun bool) error {
	target := hostView(a.NavidromeURL, a.NavidromeHostGateway)
	if dryRun {
		fmt.Fprintln(out, ui.Styles.Dim.Render("• dry run: the Navidrome login at "+target+" was not checked"))
		return nil
	}
	if s.NavidromePassword == "" {
		return fmt.Errorf("an existing Navidrome needs its password: --navidrome-password or SONGSTRESS_NAVIDROME_PASSWORD")
	}
	fmt.Fprintln(out, ui.Styles.Dim.Render("• Checking your Navidrome at "+target+"…"))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := navidrome.Check(ctx, nil, target, a.NavidromeUsername, s.NavidromePassword)
	if err == nil {
		return nil
	}
	hint := ""
	switch navidrome.Reason(err) {
	case navidrome.Unreachable:
		hint = "\n  check the URL and that Navidrome is running (the installer checks it from this machine)"
	case navidrome.Auth:
		hint = "\n  check --navidrome-username / --navidrome-password"
	case navidrome.NotAdmin:
		hint = "\n  use an admin account (Navidrome → Users), or make this one an admin"
	}
	return fmt.Errorf("not installing: %w%s", err, hint)
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
