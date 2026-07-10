// Package wizard is the interactive install flow: huh v2 forms themed for
// Songstress, filling the same deploy.Answers the --yes flags fill.
package wizard

import (
	"fmt"
	"os"
	"slices"

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
			huh.NewInput().Title("Admin email").Description("Dashboard login; password is generated.").Value(&a.AdminEmail),
			huh.NewConfirm().
				Title("Anonymous diagnostics?").
				Description("Deployment heartbeat, performance, errors — helps development.").
				Value(&a.Telemetry),
		),
	).WithTheme(themed()).WithAccessible(os.Getenv("ACCESSIBLE") != "")

	if err := form.Run(); err != nil {
		return a, s, err
	}

	if _, err := fmt.Sscanf(port, "%d", &a.Port); err != nil || a.Port < 1 || a.Port > 65535 {
		return a, s, fmt.Errorf("invalid port %q", port)
	}
	a.Discovery = has("discovery")
	a.NoAVX2 = a.Discovery && !pre.Host.HasAVX2
	a.VPN = has("vpn")
	a.HTTPS = has("https")
	a.Tailscale = has("tailscale")
	return a, s, nil
}
