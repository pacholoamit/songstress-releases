package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/execute"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
	"github.com/pacholoamit/songstress-releases/cli/internal/ui"
	"github.com/pacholoamit/songstress-releases/cli/internal/verify"
)

type installFlags struct {
	Yes, DryRun     bool
	InstallDir      string
	MusicDir        string
	Port            int
	TZ              string
	Components      string
	WGPrivateKey    string
	AdminEmail      string
	AdminPassword   string
	SkipAdminSeed   bool
	Telemetry       bool
	SongstressImage string
	PublicURL       string
	// EXISTING mode: point Songstress at a Navidrome the operator runs.
	NavidromeURL       string
	NavidromeUsername  string
	NavidromePassword  string
	NavidromePublicURL string
}

func init() {
	f := installFlags{}
	c := &cobra.Command{
		Use:   "install",
		Short: "Set up a Songstress server (interactive wizard, or --yes for scripted installs)",
		Long: `Set up a Songstress server, or update the one already in --install-dir.

Re-running is safe: passwords, keys and your data are kept, and the earlier
answers are the defaults. Without --navidrome-url the stack runs its own
Navidrome; with it, Songstress uses yours (an admin account, checked before
anything is written). Success means the server answers, its worker reports in,
and it can sign in to Navidrome — not just that a container started.

Acquisition providers work out of the box. To override one of the image's
built-in keys, set SONGSTRESS_ANTRA_API_KEY, SONGSTRESS_COMMUNITY_API_KEY,
SONGSTRESS_QOBUZ_APP_ID or SONGSTRESS_QOBUZ_APP_SECRET in the environment:
it is written to .env and kept by later re-runs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.NavidromePassword == "" {
				f.NavidromePassword = os.Getenv("SONGSTRESS_NAVIDROME_PASSWORD")
			}
			return runInstall(cmd, f)
		},
	}
	c.Flags().BoolVar(&f.Yes, "yes", false, "non-interactive: take answers from flags (and the previous install)")
	c.Flags().BoolVar(&f.DryRun, "dry-run", false, "print the generated files (secrets hidden) without writing or starting anything")
	c.Flags().StringVar(&f.InstallDir, "install-dir", defaultInstallDir(), "deployment directory")
	c.Flags().StringVar(&f.MusicDir, "music-dir", "", "music library path (required with --yes on a fresh install)")
	c.Flags().IntVar(&f.Port, "port", 8090, "dashboard port")
	c.Flags().StringVar(&f.TZ, "tz", detectTZ(), "timezone")
	c.Flags().StringVar(&f.Components, "components", "", "comma list: discovery,vpn")
	c.Flags().StringVar(&f.WGPrivateKey, "wg-private-key", "", "WireGuard private key (with components=vpn)")
	c.Flags().StringVar(&f.AdminEmail, "admin-email", "admin@songstress.local", "admin sign-in email (web/desktop/mobile + dashboard)")
	c.Flags().StringVar(&f.AdminPassword, "admin-password", "", "set the admin password instead of generating one (first install only)")
	c.Flags().BoolVar(&f.SkipAdminSeed, "skip-admin-seed", false, "leave the admin unset — create it in-app on first connect")
	c.Flags().BoolVar(&f.Telemetry, "telemetry", true, "anonymous diagnostics")
	c.Flags().StringVar(&f.SongstressImage, "songstress-image", "", "override the pinned songstress image (testing)")
	c.Flags().StringVar(&f.PublicURL, "public-url", "", "SONGSTRESS_PUBLIC_URL — where your proxy/tunnel serves Songstress, for emailed links")
	c.Flags().StringVar(&f.NavidromeURL, "navidrome-url", "", "use your existing Navidrome at this URL instead of running one")
	c.Flags().StringVar(&f.NavidromeUsername, "navidrome-username", "", "existing Navidrome: an admin account")
	c.Flags().StringVar(&f.NavidromePassword, "navidrome-password", "", "existing Navidrome: its password (or set SONGSTRESS_NAVIDROME_PASSWORD)")
	c.Flags().StringVar(&f.NavidromePublicURL, "navidrome-public-url", "", "existing Navidrome: where your devices reach it (servers before v0.32 stream from there)")
	rootCmd.AddCommand(c)
}

func defaultInstallDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./songstress"
	}
	return filepath.Join(home, "songstress")
}

func detectTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if lp, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(lp, "zoneinfo/"); i != -1 {
			return lp[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// answersFromFlags is the --yes path: same Answers the wizard produces.
func answersFromFlags(f installFlags) (deploy.Answers, error) {
	a := deploy.Answers{
		InstallDir: f.InstallDir, MusicDir: f.MusicDir, Port: f.Port, TZ: f.TZ,
		PUID: os.Getuid(), PGID: os.Getgid(),
		Telemetry: f.Telemetry, AdminEmail: f.AdminEmail, SkipAdminSeed: f.SkipAdminSeed,
		PublicURL: f.PublicURL, NavidromeUsername: f.NavidromeUsername,
		NavidromePublicURL: f.NavidromePublicURL,
	}
	if a.MusicDir == "" {
		return a, fmt.Errorf("--music-dir is required with --yes")
	}
	if st, err := os.Stat(a.MusicDir); err != nil || !st.IsDir() {
		return a, fmt.Errorf("--music-dir %s is not a directory", a.MusicDir)
	}
	if a.InstallDir == "" {
		return a, fmt.Errorf("--install-dir is required")
	}
	if f.AdminPassword != "" && f.SkipAdminSeed {
		return a, fmt.Errorf("--admin-password and --skip-admin-seed are mutually exclusive")
	}
	if f.AdminPassword != "" && len(f.AdminPassword) < 10 {
		return a, fmt.Errorf("--admin-password must be at least 10 characters")
	}
	for _, c := range strings.Split(f.Components, ",") {
		switch strings.TrimSpace(c) {
		case "":
		case "discovery":
			a.Discovery = true
		case "vpn":
			a.VPN = true
		default:
			return a, fmt.Errorf("unknown component %q (valid: discovery,vpn)", c)
		}
	}
	if f.NavidromeURL != "" {
		u, gw, err := containerNavidromeURL(f.NavidromeURL)
		if err != nil {
			return a, err
		}
		a.NavidromeURL, a.NavidromeHostGateway = u, gw
		if a.NavidromeUsername == "" {
			return a, fmt.Errorf("--navidrome-url needs --navidrome-username (a Navidrome admin)")
		}
	}
	return a, nil
}

func runInstall(cmd *cobra.Command, f installFlags) error {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, ui.Banner())

	m, err := deploy.LoadManifest()
	if err != nil {
		return err
	}
	prev, err := deploy.LoadExisting(f.InstallDir)
	if err != nil {
		return fmt.Errorf("reading the install in %s: %w", f.InstallDir, err)
	}
	if prev.HasLock {
		inheritAnswers(&f, prev.Lock.Answers, cmd.Flags().Changed)
	}
	pinned := m.Images["songstress"]
	image, note := songstressImage(pinned, prev.Lock.Pins["songstress"], f.SongstressImage)
	if note != "" {
		fmt.Fprintln(out, ui.Styles.Dim.Render("• "+note))
	}
	m.Images["songstress"] = image
	legacy := deploy.LegacyServer(image)

	pre, err := preflight.Run(preflight.DefaultRunner, portsToCheck(f, legacy, prev.Found()))
	if err != nil {
		return err
	}
	for _, n := range pre.Notes {
		fmt.Fprintln(out, ui.Styles.Warn.Render("• "+n))
	}
	if !f.DryRun && (!pre.DockerOK || !pre.ComposeOK) {
		return fmt.Errorf("docker with the compose plugin is required — install Docker Engine (https://docs.docker.com/engine/install/) and re-run")
	}

	a, given, err := collectAnswers(f, pre, prev, legacy)
	if err != nil {
		return err
	}
	given.Extra = providerOverrides(os.Getenv)
	noteProviderOverrides(out, given.Extra, legacy)
	s := deploy.MergeSecrets(prev.Secrets(), given, !a.ManagedNavidrome())
	if prev.Found() && given.AdminPassword != "" && given.AdminPassword != s.AdminPassword {
		fmt.Fprintln(out, ui.Styles.Warn.Render("• the admin account already exists — --admin-password only applies to a first install; change it in the app"))
	}
	if !a.ManagedNavidrome() {
		if err := checkExistingNavidrome(cmd.Context(), out, a, s, f.DryRun); err != nil {
			return err
		}
	}
	adminChosen := !prev.Found() && !a.SkipAdminSeed && given.AdminPassword != ""
	keptAdmin := prev.Secrets().AdminPassword != ""
	if err := mintSecrets(&s, a.SkipAdminSeed); err != nil {
		return err
	}
	if f.DryRun {
		return printDryRun(out, a, s, m)
	}
	r, err := deploy.Generate(a, s, m)
	if err != nil {
		return err
	}

	if err := writeDeployment(a, r); err != nil {
		return err
	}
	if err := deploy.WriteLock(a.InstallDir, newLock(prev, a, m)); err != nil {
		return err
	}

	report := func(line string) { fmt.Fprintln(out, ui.Styles.Dim.Render("• "+line)) }
	// Only the manifest's pins are guaranteed to be in a registry; anything else
	// (an override, or an image kept from the previous install) may be local.
	if err := execute.Up(a.InstallDir, r.ComposeArgs, preflight.DefaultRunner, report, image != pinned); err != nil {
		return err
	}
	if err := verifyStack(cmd.Context(), a, s, report); err != nil {
		return fmt.Errorf("%w\nlogs: cd %s && docker compose %s logs songstress", err, a.InstallDir, strings.Join(r.ComposeArgs, " "))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, execute.PS(a.InstallDir, r.ComposeArgs, preflight.DefaultRunner))
	printSummary(out, a, prev.Found(), keptAdmin, adminChosen)
	return nil
}

// printDryRun shows the files an install would write, every secret in .env
// replaced by deploy.Hidden: on a re-run they are the live passwords and keys.
func printDryRun(out io.Writer, a deploy.Answers, s deploy.Secrets, m deploy.Manifest) error {
	r, err := deploy.Generate(a, s.Redacted(), m)
	if err != nil {
		return err
	}
	var names []string
	for name := range r.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(out, "\n%s\n", ui.Styles.Title.Render("── "+name+" "+strings.Repeat("─", 40)))
		fmt.Fprintln(out, string(r.Files[name]))
	}
	fmt.Fprintln(out, ui.Styles.Dim.Render("dry run — nothing written, secrets shown as "+deploy.Hidden+"; compose args: docker compose "+strings.Join(r.ComposeArgs, " ")))
	return nil
}

func printSummary(out io.Writer, a deploy.Answers, rerun, keptAdmin, adminChosen bool) {
	url := fmt.Sprintf("http://localhost:%d", a.Port)
	head := "✓ Songstress is up — open " + url
	if rerun {
		head = "✓ Songstress is updated and up — open " + url
	}
	fmt.Fprintln(out, ui.Styles.Ok.Render(head))
	dim := func(line string) { fmt.Fprintln(out, ui.Styles.Dim.Render(line)) }
	// Songstress is invite-only: the admin pair below is the operator's real
	// sign-in on web/desktop/mobile, not just the /_/ dashboard.
	switch {
	case a.SkipAdminSeed && rerun:
		dim("  sign in with the admin you created in the app")
	case a.SkipAdminSeed:
		dim("  finish setup in the app: open " + url + " and create your admin")
	case keptAdmin:
		dim("  your sign-in is unchanged (" + a.AdminEmail + ", password in " + filepath.Join(a.InstallDir, ".env") + ")")
	default:
		dim("  sign in at " + url)
		dim("  email:    " + a.AdminEmail)
		if adminChosen {
			dim("  password: (the one you chose)")
		} else {
			dim("  password: stored in " + filepath.Join(a.InstallDir, ".env"))
		}
	}
	if a.ManagedNavidrome() {
		dim("  music server: the bundled Navidrome (its admin password is in .env and never changes)")
	} else {
		dim("  music server: your Navidrome at " + a.NavidromeURL + " (as " + a.NavidromeUsername + ")")
	}
	dim("  files + songstress.lock.json in " + a.InstallDir + " — re-run `songstress install` to change anything")
	// Access networking is the operator's own; point them at the one setting
	// the server needs to make emailed invite/reset links resolve off-host.
	if a.PublicURL == "" {
		dim("  reaching it from outside? front " + url + " with your reverse proxy, tunnel or tailnet,")
		dim("  then re-run with --public-url https://… so emailed links resolve")
	}
}

func newLock(prev deploy.Existing, a deploy.Answers, m deploy.Manifest) deploy.Lock {
	now := time.Now().UTC().Format(time.RFC3339)
	created := now
	if prev.HasLock && prev.Lock.CreatedAt != "" {
		created = prev.Lock.CreatedAt
	}
	return deploy.Lock{
		CLIVersion: Version,
		CreatedAt:  created,
		UpdatedAt:  now,
		Platform:   runtime.GOOS + "/" + runtime.GOARCH,
		Answers:    a,
		Pins:       m.Images,
	}
}

func verifyStack(ctx context.Context, a deploy.Answers, s deploy.Secrets, report func(string)) error {
	email, password, err := superuserLogin(ctx, a, s)
	if err != nil {
		return err
	}
	stack := verify.Stack{
		Base:     fmt.Sprintf("http://127.0.0.1:%d", a.Port),
		Email:    email,
		Password: password,
		Say:      report,
	}
	return stack.Run(ctx, verify.Budget{})
}

func mintSecrets(s *deploy.Secrets, skipAdmin bool) error {
	gen := func(target *string) error {
		if *target != "" {
			return nil
		}
		v, err := deploy.RandomSecret(24)
		if err != nil {
			return err
		}
		*target = v
		return nil
	}
	targets := []*string{&s.NavidromePassword, &s.AudioMuseToken, &s.AudioMusePassword, &s.AudioMuseDB}
	if !skipAdmin {
		// Skip mode writes an empty admin pair, so generating one would be dead weight.
		targets = append(targets, &s.AdminPassword)
	}
	for _, t := range targets {
		if err := gen(t); err != nil {
			return err
		}
	}
	return nil
}
