package cmd

import (
	"fmt"
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
	"github.com/pacholoamit/songstress-releases/cli/internal/wizard"
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
}

func init() {
	f := installFlags{}
	c := &cobra.Command{
		Use:   "install",
		Short: "Set up a Songstress server (interactive wizard, or --yes for scripted installs)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInstall(cmd, f)
		},
	}
	c.Flags().BoolVar(&f.Yes, "yes", false, "non-interactive: take answers from flags")
	c.Flags().BoolVar(&f.DryRun, "dry-run", false, "print the generated files without writing or starting anything")
	c.Flags().StringVar(&f.InstallDir, "install-dir", defaultInstallDir(), "deployment directory")
	c.Flags().StringVar(&f.MusicDir, "music-dir", "", "music library path (required with --yes)")
	c.Flags().IntVar(&f.Port, "port", 8090, "dashboard port")
	c.Flags().StringVar(&f.TZ, "tz", detectTZ(), "timezone")
	c.Flags().StringVar(&f.Components, "components", "", "comma list: discovery,vpn")
	c.Flags().StringVar(&f.WGPrivateKey, "wg-private-key", "", "WireGuard private key (with components=vpn)")
	c.Flags().StringVar(&f.AdminEmail, "admin-email", "admin@songstress.local", "admin sign-in email (web/desktop/mobile + dashboard)")
	c.Flags().StringVar(&f.AdminPassword, "admin-password", "", "set the admin password instead of generating one")
	c.Flags().BoolVar(&f.SkipAdminSeed, "skip-admin-seed", false, "leave the admin unset — create it in-app on first connect")
	c.Flags().BoolVar(&f.Telemetry, "telemetry", true, "anonymous diagnostics")
	c.Flags().StringVar(&f.SongstressImage, "songstress-image", "", "override the pinned songstress image (testing)")
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
	}
	if a.MusicDir == "" {
		return a, fmt.Errorf("--music-dir is required with --yes")
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
	if a.VPN && f.WGPrivateKey == "" {
		return a, fmt.Errorf("components=vpn requires --wg-private-key")
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
	if f.SongstressImage != "" {
		m.Images["songstress"] = f.SongstressImage
	}

	ports := []int{f.Port, 4533}
	pre, err := preflight.Run(preflight.DefaultRunner, ports)
	if err != nil {
		return err
	}
	for _, n := range pre.Notes {
		fmt.Fprintln(out, ui.Styles.Warn.Render("• "+n))
	}
	if !f.DryRun && (!pre.DockerOK || !pre.ComposeOK) {
		return fmt.Errorf("docker with the compose plugin is required — install Docker Engine (https://docs.docker.com/engine/install/) and re-run")
	}

	var a deploy.Answers
	var s deploy.Secrets
	if f.Yes || !ui.IsInteractive() {
		if a, err = answersFromFlags(f); err != nil {
			return err
		}
		s.WGPrivateKey = f.WGPrivateKey
		s.AdminPassword = f.AdminPassword // set ⇒ "choose"; empty ⇒ generated below
	} else {
		defaults := deploy.Answers{
			InstallDir: f.InstallDir, MusicDir: f.MusicDir, Port: f.Port, TZ: f.TZ,
			PUID: os.Getuid(), PGID: os.Getgid(), Telemetry: true, AdminEmail: f.AdminEmail,
		}
		if a, s, err = wizard.Run(pre, defaults); err != nil {
			return err
		}
	}
	if a.Discovery && !pre.Host.HasAVX2 {
		a.NoAVX2 = true
	}

	// A password already in hand here means the operator chose it (flag or
	// wizard); otherwise mintSecrets generates one (unless the admin is skipped).
	adminChosen := !a.SkipAdminSeed && s.AdminPassword != ""
	if err := mintSecrets(&s, a.SkipAdminSeed); err != nil {
		return err
	}
	r, err := deploy.Generate(a, s, m)
	if err != nil {
		return err
	}

	if f.DryRun {
		var names []string
		for name := range r.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(out, "\n%s\n", ui.Styles.Title.Render("── "+name+" "+strings.Repeat("─", 40)))
			fmt.Fprintln(out, string(r.Files[name]))
		}
		fmt.Fprintln(out, ui.Styles.Dim.Render("dry run — nothing written; compose args: docker compose "+strings.Join(r.ComposeArgs, " ")))
		return nil
	}

	if err := writeDeployment(a.InstallDir, r); err != nil {
		return err
	}
	lock := deploy.Lock{
		CLIVersion: Version,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Platform:   runtime.GOOS + "/" + runtime.GOARCH,
		Answers:    a,
		Pins:       m.Images,
	}
	if err := deploy.WriteLock(a.InstallDir, lock); err != nil {
		return err
	}

	report := func(line string) { fmt.Fprintln(out, ui.Styles.Dim.Render("• "+line)) }
	if err := execute.Up(a.InstallDir, r.ComposeArgs, preflight.DefaultRunner, report); err != nil {
		return err
	}
	report("Waiting for the dashboard to come up…")
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/api/health", a.Port)
	if !a.VPN {
		if err := execute.PollHTTP(healthURL, 60, 2*time.Second); err != nil {
			return fmt.Errorf("stack started but the dashboard never became healthy: %w\ncheck: docker compose --project-directory %s %s logs", err, a.InstallDir, strings.Join(r.ComposeArgs, " "))
		}
	} else {
		// Reachability differs behind the tunnel; report container states instead.
		time.Sleep(5 * time.Second)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, execute.PS(a.InstallDir, r.ComposeArgs, preflight.DefaultRunner))
	url := fmt.Sprintf("http://localhost:%d", a.Port)
	fmt.Fprintln(out, ui.Styles.Ok.Render("✓ Songstress is up — open "+url))
	// Songstress is invite-only: the admin pair below is the operator's real
	// sign-in on web/desktop/mobile, not just the /_/ dashboard.
	if a.SkipAdminSeed {
		fmt.Fprintln(out, ui.Styles.Dim.Render("  finish setup in the app: open "+url+" and create your admin"))
	} else {
		fmt.Fprintln(out, ui.Styles.Dim.Render("  sign in at "+url))
		fmt.Fprintln(out, ui.Styles.Dim.Render("  email:    "+a.AdminEmail))
		if adminChosen {
			fmt.Fprintln(out, ui.Styles.Dim.Render("  password: (the one you chose)"))
		} else {
			fmt.Fprintln(out, ui.Styles.Dim.Render("  password: stored in "+a.InstallDir+"/.env"))
		}
	}
	fmt.Fprintln(out, ui.Styles.Dim.Render("  files + songstress.lock.json in "+a.InstallDir))
	// Access networking is the operator's own; point them at the one env var the
	// server needs to make emailed invite/reset links resolve off-host.
	fmt.Fprintln(out, ui.Styles.Dim.Render("  reaching it from outside? front "+url+" with your reverse proxy, tunnel or tailnet,"))
	fmt.Fprintln(out, ui.Styles.Dim.Render("  then set SONGSTRESS_PUBLIC_URL in "+filepath.Join(a.InstallDir, ".env")+" so email links resolve"))
	return nil
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

func writeDeployment(dir string, r deploy.Rendered) error {
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return err
	}
	for name, content := range r.Files {
		mode := os.FileMode(0o644)
		if name == ".env" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, mode); err != nil {
			return err
		}
	}
	return nil
}
