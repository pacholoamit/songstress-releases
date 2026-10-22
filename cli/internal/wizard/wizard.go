// Package wizard is the interactive install flow: huh v2 forms themed for
// Songstress, filling the same deploy.Answers the --yes flags fill.
package wizard

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

// Options tune the flow to what is already installed and what is pinned.
type Options struct {
	// Rerun: an install exists in the directory. Its admin already exists, so
	// the admin password is neither asked for nor changed.
	Rerun bool
	// LegacyMedia: the pinned server (v0.31 and older) streams by redirecting
	// players to Navidrome, so an existing Navidrome needs a device-reachable URL.
	LegacyMedia bool
	// PortInUse reports a host port that something other than this install
	// holds (nil: no check). Ports are checked where they're typed, so a taken
	// one is fixed in place instead of failing `docker compose up`.
	PortInUse func(int) bool
	// KeepNavidromePassword: the install already uses an existing Navidrome,
	// whose password .env holds — a blank answer keeps it.
	KeepNavidromePassword bool
}

// DefaultNavidromePort is the bundled Navidrome's port on the host unless the
// operator picks another (NAVIDROME_PORT).
const DefaultNavidromePort = "4533"

// state is the form-bound scratch the answers are derived from afterwards.
type state struct {
	components   []string
	port         string
	passwordMode string // generate | choose | skip
	server       string // managed | existing
	enableSMTP   bool
	smtpPort     string
	smtpTo       string // blank sentinel; derived from the typed admin email AFTER the form
	smtpStartTLS bool

	// publishNavidrome: the bundled Navidrome's port reaches the host, for
	// other Subsonic apps.
	publishNavidrome bool
	// ndPort is the bundled Navidrome's host port; asked for only when the
	// current one is taken (ndPortTaken).
	ndPort      string
	ndPortTaken bool
}

func (st *state) has(c string) bool { return slices.Contains(st.components, c) }

// AskInstallDir asks where the install lives — first, and on its own, because
// an install already in that directory decides every other default (and that
// its passwords are kept).
func AskInstallDir(def string) (string, error) {
	dir := def
	err := runForm(huh.NewGroup(
		huh.NewInput().
			Title("Install directory").
			Description("Compose files, settings and data live here. Pick an existing install's directory to update it.").
			Value(&dir).
			Validate(func(p string) error {
				if strings.TrimSpace(p) == "" {
					return fmt.Errorf("enter a directory")
				}
				return nil
			}),
	))
	if err != nil {
		return "", err
	}
	return deploy.AbsPath(dir)
}

// Run collects Answers (and the user-supplied VPN key and existing-Navidrome
// password) with defaults derived from preflight and a previous install.
// Generated secrets are NOT collected here — the install command mints those.
//
// Access networking is deliberately absent: the stack publishes its port and
// the operator fronts it with their own proxy, tunnel, or tailnet.
func Run(pre preflight.Result, d deploy.Answers, o Options) (deploy.Answers, deploy.Secrets, error) {
	a, s := d, deploy.Secrets{}
	st := &state{
		port: fmt.Sprintf("%d", a.Port), passwordMode: "generate", server: "managed",
		smtpPort: "587", smtpStartTLS: true, enableSMTP: a.SMTPHost != "",
		publishNavidrome: !a.NoNavidromePort,
		ndPort:           firstNonEmpty(a.Keep["NAVIDROME_PORT"], DefaultNavidromePort),
	}
	if p, err := strconv.Atoi(st.ndPort); err == nil && o.PortInUse != nil {
		st.ndPortTaken = o.PortInUse(p)
	}
	if a.SMTPPort != 0 {
		st.smtpPort = fmt.Sprintf("%d", a.SMTPPort)
	}
	discoveryDefault := pre.MemoryGB() >= preflight.DiscoveryMemGB || a.Discovery
	if discoveryDefault {
		st.components = append(st.components, "discovery")
	}
	if a.VPN {
		st.components = append(st.components, "vpn")
	}
	if !a.ManagedNavidrome() {
		st.server = "existing"
	}

	groups := []*huh.Group{basicsGroup(&a), componentsGroup(st, discoveryDefault), vpnGroup(st, &s)}
	groups = append(groups, serverGroups(st, &a, &s, o)...)
	groups = append(groups, adminGroups(st, &a, &s, o)...)
	groups = append(groups, smtpGroups(st, &a, &s)...)
	if err := runForm(groups...); err != nil {
		return a, s, err
	}
	return finish(st, a, s, pre, o)
}

// AskNavidrome asks again for an existing Navidrome's address and login after
// the installer's check of them failed (the caller has printed why), so a
// typo costs one answer instead of the whole wizard. rawURL is the address as
// this host reaches it.
func AskNavidrome(rawURL, user, password *string) error {
	return runForm(huh.NewGroup(
		huh.NewNote().Title("Fix the Navidrome address or login (the problem is above)"),
		huh.NewInput().Title("Navidrome URL").Value(rawURL).Validate(validURL),
		huh.NewInput().
			Title("Navidrome admin username").
			Value(user).
			Validate(nonEmpty("the username")),
		huh.NewInput().
			Title("Navidrome password").
			EchoMode(huh.EchoModePassword).
			Value(password).
			Validate(nonEmpty("the password")),
	))
}

// ErrCancelled is a wizard the operator left with ctrl+c or esc. Every form
// runs before anything is written.
var ErrCancelled = errors.New("setup cancelled — nothing was written")

// runForm runs the groups as one form.
func runForm(groups ...*huh.Group) error {
	err := huh.NewForm(groups...).WithTheme(themed()).WithAccessible(os.Getenv("ACCESSIBLE") != "").Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCancelled
	}
	return err
}

func basicsGroup(a *deploy.Answers) *huh.Group {
	return huh.NewGroup(
		huh.NewInput().
			Title("Music library path").
			Description("Your music folder. Songstress writes to it only when you download or upgrade music.").
			Value(&a.MusicDir).
			Validate(func(p string) error {
				abs, err := deploy.AbsPath(p)
				if err != nil {
					return err
				}
				if st, err := os.Stat(abs); err != nil || !st.IsDir() {
					return fmt.Errorf("not a directory: %s", abs)
				}
				return nil
			}),
	)
}

func componentsGroup(st *state, discoveryDefault bool) *huh.Group {
	options := []huh.Option[string]{
		huh.NewOption(fmt.Sprintf("Discovery — sonic analysis and instant mixes (AudioMuse-AI, wants ~%d GB RAM)", preflight.DiscoveryMemGB), "discovery").Selected(discoveryDefault),
		huh.NewOption("VPN — route downloads through your WireGuard VPN (gluetun)", "vpn").Selected(st.has("vpn")),
	}
	const description = "Optional; re-run the installer to change them."
	return huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Components").
			Description(description).
			Options(options...).
			// huh sizes an unset height to the options minus the title, which
			// showed one option at a time: VPN sat out of sight below Discovery.
			Height(2 + len(options) + 1).
			Value(&st.components),
	)
}

func vpnGroup(st *state, s *deploy.Secrets) *huh.Group {
	return huh.NewGroup(
		huh.NewInput().
			Title("WireGuard private key").
			Description("From your VPN provider's WireGuard config. Leave blank on a re-run to keep the current one.").
			EchoMode(huh.EchoModePassword).
			Value(&s.WGPrivateKey),
	).WithHideFunc(func() bool { return !st.has("vpn") })
}

// serverGroups: the stack's own Navidrome (FRESH) or one the operator already
// runs (EXISTING). The existing login is verified by the install command
// before anything is written.
func serverGroups(st *state, a *deploy.Answers, s *deploy.Secrets, o Options) []*huh.Group {
	choose := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Music server").
			Description("Songstress streams and scans through Navidrome.").
			Options(
				huh.NewOption("Set one up for me (recommended)", "managed"),
				huh.NewOption("Use my existing Navidrome", "existing"),
			).
			Value(&st.server),
	)
	password := huh.NewInput().
		Title("Navidrome password").
		EchoMode(huh.EchoModePassword).
		Value(&s.NavidromePassword)
	if o.KeepNavidromePassword {
		password = password.Description("Leave blank to keep the current one.")
	} else {
		password = password.Validate(nonEmpty("the password"))
	}
	existing := huh.NewGroup(
		huh.NewInput().
			Title("Navidrome URL").
			Description("As the Songstress container reaches it, e.g. http://192.168.1.10:4533. A localhost URL is rewritten to host.docker.internal.").
			Value(&a.NavidromeURL).
			Validate(validURL),
		huh.NewInput().
			Title("Navidrome admin username").
			Description("Must be a Navidrome admin: Songstress starts scans and creates accounts for the people you invite.").
			Value(&a.NavidromeUsername).
			Validate(nonEmpty("the username")),
		password,
	).WithHideFunc(func() bool { return st.server != "existing" })
	// A legacy server streams through the bundled Navidrome's port, so there
	// it isn't a choice.
	managed := huh.NewGroup(
		huh.NewConfirm().
			Title("Publish Navidrome's port for other Subsonic apps?").
			Description("Songstress serves all media itself and doesn't need it. Yes keeps the bundled Navidrome reachable on a port of its own.").
			Value(&st.publishNavidrome),
	).WithHideFunc(func() bool { return st.server != "managed" || o.LegacyMedia })
	ndPort := huh.NewGroup(
		huh.NewInput().
			Title("Navidrome's port").
			Description("Port " + st.ndPort + " is already in use on this host. Pick a free one: other Subsonic apps reach the bundled Navidrome there.").
			Value(&st.ndPort).
			Validate(portValidator(o.PortInUse)),
	).WithHideFunc(func() bool {
		return st.server != "managed" || !(o.LegacyMedia || st.publishNavidrome) || !st.ndPortTaken
	})
	groups := []*huh.Group{choose, existing, managed, ndPort}
	if o.LegacyMedia {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().
				Title("Navidrome address for your devices").
				Description("This Songstress release streams by sending players to Navidrome directly. Blank = the URL above.").
				Value(&a.NavidromePublicURL),
		).WithHideFunc(func() bool { return st.server != "existing" }))
	}
	return groups
}

func adminGroups(st *state, a *deploy.Answers, s *deploy.Secrets, o Options) []*huh.Group {
	settings := []huh.Field{huh.NewInput().
		Title("Dashboard port").
		Description("Songstress's web app, and the address the desktop and mobile apps connect to.").
		Value(&st.port).
		Validate(portValidator(o.PortInUse))}
	if !o.Rerun {
		settings = append(settings,
			huh.NewInput().
				Title("Admin email").
				Description("Your sign-in for web, desktop & mobile (and the /_/ dashboard).").
				Value(&a.AdminEmail).
				Validate(nonEmpty("the admin email — it's your sign-in identity")),
			huh.NewSelect[string]().
				Title("Admin password").
				Description("Songstress is invite-only — this pair is your account.").
				Options(
					huh.NewOption("Generate for me (recommended)", "generate"),
					huh.NewOption("Let me choose", "choose"),
					huh.NewOption("Skip — I'll create the admin in the app on first connect", "skip"),
				).
				Value(&st.passwordMode),
		)
	}
	settings = append(settings, huh.NewConfirm().
		Title("Anonymous diagnostics?").
		Description("Deployment heartbeat, performance, errors — helps development.").
		Value(&a.Telemetry))
	choose := huh.NewGroup(
		huh.NewInput().
			Title("Admin password").
			Description("At least 10 characters.").
			EchoMode(huh.EchoModePassword).
			Value(&s.AdminPassword).
			Validate(func(p string) error {
				if len(p) < 10 {
					return fmt.Errorf("use at least 10 characters")
				}
				return nil
			}),
	).WithHideFunc(func() bool { return o.Rerun || st.passwordMode != "choose" })
	return []*huh.Group{huh.NewGroup(settings...), choose}
}

func smtpGroups(st *state, a *deploy.Answers, s *deploy.Secrets) []*huh.Group {
	return []*huh.Group{
		huh.NewGroup(
			huh.NewConfirm().
				Title("Set up email (SMTP)?").
				Description("Sends invites & password resets. Optional — add it later by re-running.").
				Value(&st.enableSMTP),
		),
		huh.NewGroup(
			huh.NewInput().Title("SMTP host").Placeholder("smtp.example.com").Value(&a.SMTPHost),
			huh.NewInput().Title("SMTP port").Value(&st.smtpPort).Validate(portValidator(nil)),
			huh.NewInput().Title("SMTP username").Value(&a.SMTPUsername),
			huh.NewInput().Title("SMTP password").Description("Leave blank on a re-run to keep the current one.").EchoMode(huh.EchoModePassword).Value(&s.SMTPPassword),
			huh.NewInput().Title("From address").Placeholder("Songstress <no-reply@example.com>").Value(&a.SMTPFrom),
			huh.NewInput().Title("Notification recipient").Description("Where notification emails go; blank uses your admin email (or none in skip mode).").Value(&st.smtpTo),
			huh.NewConfirm().Title("Use STARTTLS?").Description("Recommended for port 587.").Value(&st.smtpStartTLS),
		).WithHideFunc(func() bool { return !st.enableSMTP }),
	}
}

// finish turns the form state into Answers/Secrets.
func finish(st *state, a deploy.Answers, s deploy.Secrets, pre preflight.Result, o Options) (deploy.Answers, deploy.Secrets, error) {
	if _, err := fmt.Sscanf(st.port, "%d", &a.Port); err != nil || a.Port < 1 || a.Port > 65535 {
		return a, s, fmt.Errorf("invalid port %q", st.port)
	}
	music, err := deploy.AbsPath(a.MusicDir)
	if err != nil {
		return a, s, err
	}
	a.MusicDir = music
	switch {
	case o.Rerun:
		s.AdminPassword = "" // the existing one is kept by the install command
	case st.passwordMode == "skip":
		a.SkipAdminSeed = true
		s.AdminPassword = ""
	case st.passwordMode == "generate":
		s.AdminPassword = "" // the install command mints it
		// "choose": keep the masked value the operator typed
	}
	if st.enableSMTP {
		if _, err := fmt.Sscanf(st.smtpPort, "%d", &a.SMTPPort); err != nil || a.SMTPPort < 1 || a.SMTPPort > 65535 {
			return a, s, fmt.Errorf("invalid smtp port %q", st.smtpPort)
		}
		// Derived after the form: huh's Value() snapshots the pointee at build
		// time, so the admin email typed above isn't visible to a prefilled field.
		a.SMTPTo = deriveSMTPTo(st.smtpTo, a.AdminEmail, a.SkipAdminSeed)
		a.SMTPStartTLS = st.smtpStartTLS
	} else {
		// A toggle-off after filling fields must not leak an SMTP block into .env.
		a.SMTPHost, a.SMTPUsername, a.SMTPFrom, a.SMTPTo, a.SMTPPort = "", "", "", "", 0
		a.SMTPStartTLS = false
		s.SMTPPassword = ""
	}
	if st.server == "managed" {
		a.NavidromeURL, a.NavidromeUsername, a.NavidromePublicURL = "", "", ""
		s.NavidromePassword = "" // the managed one is minted (or kept) by the install command
	}
	// Only the bundled Navidrome has a port to keep closed, and only a current
	// server can do without it.
	a.NoNavidromePort = st.server == "managed" && !o.LegacyMedia && !st.publishNavidrome
	if st.ndPortTaken && a.NavidromePublished(o.LegacyMedia) {
		a.Keep = maps.Clone(a.Keep)
		if a.Keep == nil {
			a.Keep = map[string]string{}
		}
		a.Keep["NAVIDROME_PORT"] = strings.TrimSpace(st.ndPort)
	}
	a.Discovery = st.has("discovery")
	a.NoAVX2 = a.Discovery && !pre.Host.HasAVX2
	a.VPN = st.has("vpn")
	return a, s, nil
}

// portValidator accepts a port number that's free on this host (inUse nil:
// any port number).
func portValidator(inUse func(int) bool) func(string) error {
	return func(v string) error {
		p, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("enter a port number from 1 to 65535")
		}
		if inUse != nil && inUse(p) {
			return fmt.Errorf("port %d is already in use on this host — pick another", p)
		}
		return nil
	}
}

func validURL(v string) error {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("enter a full URL, e.g. http://192.168.1.10:4533")
	}
	return nil
}

func nonEmpty(what string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("enter %s", what)
		}
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// deriveSMTPTo resolves the notification recipient after the form has run. An
// explicitly entered address always wins; a blank field inherits the (typed)
// admin email for generate/choose, but stays blank when the admin is skipped
// (no admin email is seeded then).
func deriveSMTPTo(entered, adminEmail string, skipAdmin bool) string {
	if entered != "" {
		return entered
	}
	if skipAdmin {
		return ""
	}
	return adminEmail
}
