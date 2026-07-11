// Package wizard is the interactive install flow: huh v2 forms themed for
// Songstress, filling the same deploy.Answers the --yes flags fill.
package wizard

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

// Run collects Answers (and the user-supplied VPN/Tailscale keys) with
// defaults derived from preflight. Generated secrets are NOT collected here —
// the install command mints those.
func Run(pre preflight.Result, d deploy.Answers) (deploy.Answers, deploy.Secrets, error) {
	a, s := d, deploy.Secrets{}

	discoveryDefault := pre.Host.RAMGigabytes >= 8
	var components []string
	if discoveryDefault {
		components = append(components, "discovery")
	}
	port := fmt.Sprintf("%d", a.Port)
	passwordMode := "generate" // generate | choose | skip
	enableSMTP := false
	smtpPort := "587"
	smtpTo := "" // blank sentinel; derived from the typed admin email AFTER the form
	smtpStartTLS := true

	has := func(c string) bool { return slices.Contains(components, c) }

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Install directory").
				Description("Compose files, config, and data live here.").
				Value(&a.InstallDir),
			huh.NewInput().
				Title("Music library path").
				Description("Your existing music folder (mounted read-write for acquisition).").
				Value(&a.MusicDir).
				Validate(func(p string) error {
					st, err := os.Stat(p)
					if err != nil || !st.IsDir() {
						return fmt.Errorf("not a directory: %s", p)
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Components").
				Description("Navidrome (streaming) is always included.").
				Options(
					huh.NewOption("Discovery — sonic analysis, instant mix (AudioMuse-AI)", "discovery").Selected(discoveryDefault),
					huh.NewOption("VPN egress for acquisition (gluetun)", "vpn"),
					huh.NewOption("Domain & HTTPS (Caddy + Let's Encrypt)", "https"),
					huh.NewOption("Tailscale — serve over your tailnet", "tailscale"),
				).
				Value(&components),
		),
		huh.NewGroup(
			huh.NewInput().Title("Domain").Placeholder("music.example.com").Value(&a.Domain),
			huh.NewInput().Title("ACME email (Let's Encrypt)").Value(&a.ACMEEmail),
		).WithHideFunc(func() bool { return !has("https") }),
		huh.NewGroup(
			huh.NewInput().
				Title("WireGuard private key").
				Description("From your VPN provider's WireGuard config.").
				EchoMode(huh.EchoModePassword).
				Value(&s.WGPrivateKey),
		).WithHideFunc(func() bool { return !has("vpn") }),
		huh.NewGroup(
			huh.NewInput().
				Title("Tailscale auth key").
				Description("tskey-auth-… from the Tailscale admin console.").
				EchoMode(huh.EchoModePassword).
				Value(&s.TSAuthKey),
		).WithHideFunc(func() bool { return !has("tailscale") }),
		huh.NewGroup(
			huh.NewInput().Title("Dashboard port").Value(&port),
			huh.NewInput().
				Title("Admin email").
				Description("Your sign-in for web, desktop & mobile (and the /_/ dashboard).").
				Value(&a.AdminEmail).
				Validate(func(e string) error {
					if strings.TrimSpace(e) == "" {
						return fmt.Errorf("admin email can't be empty — it's your sign-in identity")
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("Admin password").
				Description("Songstress is invite-only — this pair is your account.").
				Options(
					huh.NewOption("Generate for me (recommended)", "generate"),
					huh.NewOption("Let me choose", "choose"),
					huh.NewOption("Skip — I'll create the admin in the app on first connect", "skip"),
				).
				Value(&passwordMode),
			huh.NewConfirm().
				Title("Anonymous diagnostics?").
				Description("Deployment heartbeat, performance, errors — helps development.").
				Value(&a.Telemetry),
		),
		huh.NewGroup(
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
		).WithHideFunc(func() bool { return passwordMode != "choose" }),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Set up email (SMTP)?").
				Description("Sends invites & password resets. Optional — add it later by re-running.").
				Value(&enableSMTP),
		),
		huh.NewGroup(
			huh.NewInput().Title("SMTP host").Placeholder("smtp.example.com").Value(&a.SMTPHost),
			huh.NewInput().Title("SMTP port").Value(&smtpPort),
			huh.NewInput().Title("SMTP username").Value(&a.SMTPUsername),
			huh.NewInput().Title("SMTP password").EchoMode(huh.EchoModePassword).Value(&s.SMTPPassword),
			huh.NewInput().Title("From address").Placeholder("Songstress <no-reply@example.com>").Value(&a.SMTPFrom),
			huh.NewInput().Title("Notification recipient").Description("Where notification emails go; blank uses your admin email (or none in skip mode).").Value(&smtpTo),
			huh.NewConfirm().Title("Use STARTTLS?").Description("Recommended for port 587.").Value(&smtpStartTLS),
		).WithHideFunc(func() bool { return !enableSMTP }),
	).WithTheme(themed()).WithAccessible(os.Getenv("ACCESSIBLE") != "")

	if err := form.Run(); err != nil {
		return a, s, err
	}

	if _, err := fmt.Sscanf(port, "%d", &a.Port); err != nil || a.Port < 1 || a.Port > 65535 {
		return a, s, fmt.Errorf("invalid port %q", port)
	}
	switch passwordMode {
	case "skip":
		a.SkipAdminSeed = true
		s.AdminPassword = ""
	case "generate":
		s.AdminPassword = "" // the install command mints it
		// "choose": keep the masked value the operator typed
	}
	if enableSMTP {
		if _, err := fmt.Sscanf(smtpPort, "%d", &a.SMTPPort); err != nil || a.SMTPPort < 1 || a.SMTPPort > 65535 {
			return a, s, fmt.Errorf("invalid smtp port %q", smtpPort)
		}
		// Derived after the form: huh's Value() snapshots the pointee at build
		// time, so the admin email typed above isn't visible to a prefilled field.
		a.SMTPTo = deriveSMTPTo(smtpTo, a.AdminEmail, a.SkipAdminSeed)
		a.SMTPStartTLS = smtpStartTLS
	} else {
		// A toggle-off after filling fields must not leak an SMTP block into .env.
		a.SMTPHost, a.SMTPUsername, a.SMTPFrom, a.SMTPTo, a.SMTPPort = "", "", "", "", 0
		a.SMTPStartTLS = false
		s.SMTPPassword = ""
	}
	a.Discovery = has("discovery")
	a.NoAVX2 = a.Discovery && !pre.Host.HasAVX2
	a.VPN = has("vpn")
	a.HTTPS = has("https")
	a.Tailscale = has("tailscale")
	return a, s, nil
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
