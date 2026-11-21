// Package wizard is the interactive install flow: huh v2 forms themed for
// Songstress, filling the same deploy.Answers the --yes flags fill.
package wizard

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// Options tune the flow to what is already installed.
type Options struct {
	// Rerun: an install exists in the directory. Its admin already exists, so
	// the admin password is neither asked for nor changed.
	Rerun bool
	// PortInUse reports a host port that something other than this install
	// holds (nil: no check). Ports are checked where they're typed, so a taken
	// one is fixed in place instead of failing `docker compose up`.
	PortInUse func(int) bool
	// VoicePortInUse is PortInUse for Jam voice's port, over TCP or UDP.
	VoicePortInUse func(int) bool
}

// state is the form-bound scratch the answers are derived from afterwards.
type state struct {
	vpn          bool // route downloads through the VPN
	port         string
	voicePort    string
	passwordMode string // generate | choose | skip
	enableSMTP   bool
	smtpPort     string
	smtpTo       string // blank sentinel; derived from the typed admin email AFTER the form
	smtpStartTLS bool
}

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

// Run collects Answers (and the user-supplied VPN key and SMTP password) with
// defaults from a previous install.
// Generated secrets are NOT collected here — the install command mints those.
//
// Access networking is deliberately absent: the stack publishes its port and
// the operator fronts it with their own proxy, tunnel, or tailnet.
func Run(d deploy.Answers, o Options) (deploy.Answers, deploy.Secrets, error) {
	a, s := d, deploy.Secrets{}
	if a.VoicePort == 0 {
		a.VoicePort = deploy.DefaultVoicePort
	}
	st := &state{
		port: fmt.Sprintf("%d", a.Port), voicePort: fmt.Sprintf("%d", a.VoicePort), passwordMode: "generate",
		smtpPort: "587", smtpStartTLS: true, enableSMTP: a.SMTPHost != "",
	}
	if a.SMTPPort != 0 {
		st.smtpPort = fmt.Sprintf("%d", a.SMTPPort)
	}
	st.vpn = a.VPN

	groups := []*huh.Group{basicsGroup(&a), componentsGroup(st), vpnGroup(st, &s)}
	groups = append(groups, adminGroups(st, &a, &s, o)...)
	groups = append(groups, smtpGroups(st, &a, &s)...)
	if err := runForm(groups...); err != nil {
		return a, s, err
	}
	return finish(st, a, s, o)
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

func componentsGroup(st *state) *huh.Group {
	return huh.NewGroup(
		huh.NewConfirm().
			Title("Route downloads through your VPN?").
			Description("Acquisition leaves through your WireGuard VPN (gluetun); listening stays direct. Re-run the installer to change it.").
			Value(&st.vpn),
	)
}

func vpnGroup(st *state, s *deploy.Secrets) *huh.Group {
	return huh.NewGroup(
		huh.NewInput().
			Title("WireGuard private key").
			Description("From your VPN provider's WireGuard config. Leave blank on a re-run to keep the current one.").
			EchoMode(huh.EchoModePassword).
			Value(&s.WGPrivateKey),
	).WithHideFunc(func() bool { return !st.vpn })
}

func adminGroups(st *state, a *deploy.Answers, s *deploy.Secrets, o Options) []*huh.Group {
	settings := []huh.Field{
		huh.NewInput().
			Title("Dashboard port").
			Description("Songstress's web app, and the address the desktop and mobile apps connect to.").
			Value(&st.port).
			Validate(portValidator(o.PortInUse)),
		huh.NewInput().
			Title("Jam voice port").
			Description("People in a Jam talk through it: UDP, and the same TCP port for networks that block UDP.").
			Value(&st.voicePort).
			Validate(voicePortValidator(st, o.VoicePortInUse)),
	}
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
		Title("Share diagnostics?").
		Description("Errors, performance and session replays, with each signed-in person's name, username and email, so problems can be traced to the people they hit.").
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
func finish(st *state, a deploy.Answers, s deploy.Secrets, o Options) (deploy.Answers, deploy.Secrets, error) {
	if _, err := fmt.Sscanf(st.port, "%d", &a.Port); err != nil || a.Port < 1 || a.Port > 65535 {
		return a, s, fmt.Errorf("invalid port %q", st.port)
	}
	if _, err := fmt.Sscanf(st.voicePort, "%d", &a.VoicePort); err != nil {
		return a, s, fmt.Errorf("invalid Jam voice port %q", st.voicePort)
	}
	if err := deploy.VoicePortProblem(a.VoicePort, a.Port, st.vpn); err != nil {
		return a, s, fmt.Errorf("Jam voice port %d: %w", a.VoicePort, err)
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
	a.VPN = st.vpn
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

// voicePortValidator is portValidator for Jam voice's port (inUse: taken over
// TCP or UDP), which must also keep off the dashboard's port as typed above it
// and the ports the container uses itself.
func voicePortValidator(st *state, inUse func(int) bool) func(string) error {
	free := portValidator(inUse)
	return func(v string) error {
		if err := free(v); err != nil {
			return err
		}
		p, _ := strconv.Atoi(strings.TrimSpace(v))
		dashboard, _ := strconv.Atoi(strings.TrimSpace(st.port))
		return deploy.VoicePortProblem(p, dashboard, st.vpn)
	}
}

func nonEmpty(what string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("enter %s", what)
		}
		return nil
	}
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
