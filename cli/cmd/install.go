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
}

// installURL is the one-line installer: it fetches the newest CLI, which pins
// the newest tested Songstress, and runs `songstress install`.
const installURL = "https://raw.githubusercontent.com/pacholoamit/songstress-releases/main/install.sh"

func init() {
	f := installFlags{}
	c := &cobra.Command{
		Use:   "install",
		Short: "Set up a Songstress server (interactive wizard, or --yes for scripted installs)",
		Long: `Set up a Songstress server, or update the one already in --install-dir.

Needs Docker with the compose plugin. Run it in a terminal for the setup
wizard; --yes takes the answers from flags instead.

Songstress runs as one container: the music server, its datastore, the web
dashboard and the acquisition worker. Re-running is safe: passwords, keys and
your data are kept, the earlier answers are the defaults, and the files a
re-run replaces are kept in previous/. Success means the server answers, its
worker reports in, and it serves your library itself — not just that a
container started.

Acquisition providers work out of the box. To override one of the image's
built-in keys, set SONGSTRESS_ANTRA_API_KEY, SONGSTRESS_COMMUNITY_API_KEY,
SONGSTRESS_QOBUZ_APP_ID or SONGSTRESS_QOBUZ_APP_SECRET in the environment:
it is written to .env and kept by later re-runs.`,
		Example: `  # the wizard
  songstress install

  # scripted
  songstress install --yes --music-dir /srv/music

  # update an install: the same answers, the pins of this CLI
  songstress install --yes --install-dir /srv/songstress`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInstall(cmd, f)
		},
	}
	c.Flags().BoolVar(&f.Yes, "yes", false, "non-interactive: take answers from flags (and the previous install)")
	c.Flags().BoolVar(&f.DryRun, "dry-run", false, "print the generated files (secrets hidden) without writing or starting anything")
	c.Flags().StringVar(&f.InstallDir, "install-dir", defaultInstallDir(), "deployment directory")
	c.Flags().StringVar(&f.MusicDir, "music-dir", "", "music library path (required with --yes on a fresh install)")
	c.Flags().IntVar(&f.Port, "port", 8090, "dashboard port: the web app, and what the desktop and mobile apps connect to")
	c.Flags().StringVar(&f.TZ, "tz", detectTZ(), "timezone")
	c.Flags().StringVar(&f.Components, "components", "", "optional extras, comma-separated: vpn (downloads through WireGuard)")
	c.Flags().StringVar(&f.WGPrivateKey, "wg-private-key", "", "WireGuard private key (with components=vpn)")
	c.Flags().StringVar(&f.AdminEmail, "admin-email", "admin@songstress.local", "admin sign-in email (web/desktop/mobile + dashboard)")
	c.Flags().StringVar(&f.AdminPassword, "admin-password", "", "set the admin password instead of generating one (first install only)")
	c.Flags().BoolVar(&f.SkipAdminSeed, "skip-admin-seed", false, "leave the admin unset — create it in-app on first connect")
	c.Flags().BoolVar(&f.Telemetry, "telemetry", true, "anonymous diagnostics")
	c.Flags().StringVar(&f.SongstressImage, "songstress-image", "", "override the pinned songstress image (testing)")
	c.Flags().StringVar(&f.PublicURL, "public-url", "", "SONGSTRESS_PUBLIC_URL — where your proxy/tunnel serves Songstress, for emailed links")
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
		PublicURL: f.PublicURL,
	}
	if a.MusicDir == "" {
		return a, fmt.Errorf("--music-dir is required with --yes")
	}
	music, err := deploy.AbsPath(a.MusicDir)
	if err != nil {
		return a, err
	}
	a.MusicDir = music
	if st, err := os.Stat(a.MusicDir); err != nil || !st.IsDir() {
		return a, fmt.Errorf("--music-dir %s is not a directory", a.MusicDir)
	}
	if a.InstallDir == "" {
		return a, fmt.Errorf("--install-dir is required")
	}
	if a.Port < 1 || a.Port > 65535 {
		return a, fmt.Errorf("--port %d: use a port from 1 to 65535", a.Port)
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
		case "vpn":
			a.VPN = true
		default:
			return a, fmt.Errorf("unknown component %q (valid: vpn)", c)
		}
	}
	return a, nil
}

func runInstall(cmd *cobra.Command, f installFlags) error {
	r := &installRun{out: cmd.OutOrStdout(), changed: cmd.Flags().Changed, prompt: !f.Yes && ui.CanPrompt()}
	fmt.Fprintln(r.out, ui.Banner())
	if err := r.prepare(&f); err != nil {
		return err
	}
	a, s, err := r.decide(f)
	if err != nil {
		return err
	}
	if f.DryRun {
		return printDryRun(r.out, a, s, r.m)
	}
	return r.apply(cmd.Context(), a, s)
}

// installRun is one `songstress install`: what it found before asking
// anything, and how it reports.
type installRun struct {
	out     io.Writer
	changed func(flag string) bool
	prompt  bool // the wizard asks; otherwise flags answer

	prev          deploy.Existing
	m             deploy.Manifest // its songstress pin is the image this run installs
	image, pinned string
	pre           preflight.Result
	// adminChosen / keptAdmin: how the summary describes the sign-in.
	adminChosen, keptAdmin bool
}

func (r *installRun) note(line string) { fmt.Fprintln(r.out, ui.Styles.Dim.Render("• "+line)) }
func (r *installRun) warn(line string) { fmt.Fprintln(r.out, ui.Styles.Warn.Render("• "+line)) }

// prepare finds the install directory and what's already in it, picks the
// image, and checks Docker — before a single question about the stack.
func (r *installRun) prepare(f *installFlags) error {
	var err error
	if r.m, err = deploy.LoadManifest(); err != nil {
		return err
	}
	if err := resolveDirs(f, r.prompt && !r.changed("install-dir")); err != nil {
		return err
	}
	if r.prev, err = deploy.LoadExisting(f.InstallDir); err != nil {
		return fmt.Errorf("reading the install in %s: %w", f.InstallDir, err)
	}
	if r.prev.HasLock {
		inheritAnswers(f, r.prev.Lock.Answers, r.changed)
	}
	if r.prev.Found() {
		r.note("updating the install in " + f.InstallDir + " — its passwords, keys and data are kept")
		if r.prev.HadMusicServer() {
			r.note("Songstress is its own music server now, so the music server beside it leaves the stack. Its first start " +
				"imports what everyone kept there (favorites, ratings, playlists, play counts); it only reads data/navidrome, " +
				"which stays until you delete it")
		}
		if n := sideCarNote(f.InstallDir, r.prev); n != "" {
			r.note(n)
		}
	} else if r.prompt || f.Yes {
		r.note("a new install in " + f.InstallDir)
	}
	r.pinned = r.m.Images["songstress"]
	image, imageNote := songstressImage(r.pinned, r.prev.Lock.Pins["songstress"], f.SongstressImage)
	if imageNote != "" {
		r.note(imageNote)
	}
	r.image, r.m.Images["songstress"] = image, image

	if r.pre, err = preflight.Run(preflight.DefaultRunner); err != nil {
		return err
	}
	for _, n := range r.pre.Notes {
		r.warn(n)
	}
	if r.pre.Problem != nil {
		if !f.DryRun {
			return r.pre.Problem
		}
		r.warn(r.pre.Problem.Error())
	}
	return nil
}

// decide collects the answers and the secrets — the existing ones kept, the
// missing ones minted — and refuses what would fail later: a taken port.
// Nothing is written yet.
func (r *installRun) decide(f installFlags) (deploy.Answers, deploy.Secrets, error) {
	inUse := portInUse(r.prev, preflight.PortFree)
	a, given, err := collectAnswers(f, r.prev, r.prompt, inUse)
	if err != nil {
		return a, given, err
	}
	if err := busyPorts(a, inUse); err != nil {
		if !f.DryRun {
			return a, given, fmt.Errorf("not installing: %w", err)
		}
		r.warn(err.Error())
	}
	if os.Getuid() == 0 && a.PUID != 0 {
		r.note(fmt.Sprintf("running as root: the containers run as %d:%d, who owns %s", a.PUID, a.PGID, a.MusicDir))
	} else if n := musicDirNote(a.MusicDir); n != "" {
		r.warn(n)
	}
	given.Extra = providerOverrides(os.Getenv)
	noteProviderOverrides(r.out, given.Extra)
	s := deploy.MergeSecrets(r.prev.Secrets(), given)
	if r.prev.Found() && given.AdminPassword != "" && given.AdminPassword != s.AdminPassword {
		r.warn("the admin account already exists — --admin-password only applies to a first install; change it in the app")
	}
	r.adminChosen = !r.prev.Found() && !a.SkipAdminSeed && given.AdminPassword != ""
	r.keptAdmin = r.prev.Secrets().AdminPassword != ""
	return a, s, mintSecrets(&s, a)
}

// apply writes the deployment, brings it up and proves it works.
func (r *installRun) apply(ctx context.Context, a deploy.Answers, s deploy.Secrets) error {
	rendered, err := deploy.Generate(a, s, r.m)
	if err != nil {
		return err
	}
	if r.prev.Found() {
		changed := changedFiles(a.InstallDir, rendered.Files)
		r.note(describeChanges(changed))
		if len(changed) > 0 {
			if err := keepPrevious(a.InstallDir); err != nil {
				return fmt.Errorf("keeping the files this run replaces in %s: %w", filepath.Join(a.InstallDir, previousDir), err)
			}
			r.note("the files it replaces are in " + filepath.Join(a.InstallDir, previousDir))
		}
	}
	if err := writeDeployment(a, rendered); err != nil {
		return err
	}
	if err := deploy.WriteLock(a.InstallDir, newLock(r.prev, a, r.m)); err != nil {
		return err
	}

	var show execute.Shower
	if ui.Terminal() {
		show = execute.ShowOnTerminal
	}
	// Only the manifest's pins are guaranteed to be in a registry; anything else
	// (an override, or an image kept from the previous install) may be local.
	if err := execute.Up(a.InstallDir, rendered.ComposeArgs, preflight.DefaultRunner, show, r.note, r.image != r.pinned); err != nil {
		return err
	}
	if err := verifyStack(ctx, a, s, r.note); err != nil {
		return fmt.Errorf("%w\nlogs: cd %s && docker compose %s logs songstress", err, a.InstallDir, strings.Join(rendered.ComposeArgs, " "))
	}

	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, execute.PS(a.InstallDir, rendered.ComposeArgs, preflight.DefaultRunner))
	printSummary(r.out, a, outcome{
		rerun: r.prev.Found(), keptAdmin: r.keptAdmin, imports: r.prev.HadMusicServer(),
		adminChosen: r.adminChosen, lan: lanAddress(), defaultDir: a.InstallDir == defaultInstallDir(),
	})
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

// outcome is what the summary needs to know about the run.
type outcome struct {
	rerun, keptAdmin, adminChosen bool
	// imports: this first start imports what the install's previous music
	// server kept.
	imports bool
	// lan is this host's network address ("" when unknown).
	lan string
	// defaultDir: the install is in the default directory, so a bare
	// `songstress install` finds it again.
	defaultDir bool
}

func printSummary(out io.Writer, a deploy.Answers, o outcome) {
	local := fmt.Sprintf("http://localhost:%d", a.Port)
	reach := local
	head := "✓ Songstress is up — open " + local
	if o.lan != "" {
		reach = fmt.Sprintf("http://%s:%d", o.lan, a.Port)
		head += " (from another device: " + reach + ")"
	}
	if o.rerun {
		head = strings.Replace(head, "is up", "is updated and up", 1)
	}
	fmt.Fprintln(out, ui.Styles.Ok.Render(head))
	dim := func(line string) { fmt.Fprintln(out, ui.Styles.Dim.Render(line)) }
	env := filepath.Join(a.InstallDir, ".env")
	// Songstress is invite-only: the admin pair below is the operator's real
	// sign-in on web/desktop/mobile, not just the /_/ dashboard.
	switch {
	case a.SkipAdminSeed && o.rerun:
		dim("  sign in with the admin you created in the app")
	case a.SkipAdminSeed:
		dim("  finish setup now: open " + reach + " and create your admin — until you do, whoever opens it first can")
	case o.keptAdmin:
		dim("  your sign-in is unchanged (" + a.AdminEmail + ", password in " + env + ")")
	default:
		dim("  sign in at " + reach)
		dim("  email:    " + a.AdminEmail)
		if o.adminChosen {
			dim("  password: (the one you chose)")
		} else {
			dim("  password: stored in " + env + " — show it with:")
			dim("            grep SONGSTRESS_ADMIN_PASSWORD= " + env)
		}
	}
	if !o.rerun {
		dim("  desktop & mobile apps: enter " + reach + " as the server")
	}
	dim("  your library: " + a.MusicDir + " — the first scan starts by itself")
	if o.imports {
		dim("  importing from your previous music server: Activity in the app shows it running, then its report")
	}
	again := "songstress install"
	if !o.defaultDir {
		again += " --install-dir " + a.InstallDir
	}
	dim("  files + songstress.lock.json in " + a.InstallDir + " — re-run `" + again + "` to change anything")
	dim("  to update Songstress later, run the installer again (it fetches the newest CLI, then this setup):")
	dim("            curl -fsSL " + installURL + " | sh")
	// Access networking is the operator's own; point them at the one setting
	// the server needs to make emailed invite/reset links resolve off-host.
	if a.PublicURL == "" {
		dim("  reaching it from outside your network? front port " + fmt.Sprint(a.Port) + " with your reverse proxy, tunnel or tailnet,")
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

func mintSecrets(s *deploy.Secrets, a deploy.Answers) error {
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
	var targets []*string
	if !a.SkipAdminSeed {
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
