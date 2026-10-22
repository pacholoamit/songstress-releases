package deploy

// Answers is everything the wizard (or --yes flags) decides. It is persisted
// in the lock file, so it must never contain secrets — those travel in
// Secrets and land only in the generated .env.
type Answers struct {
	InstallDir string `json:"install_dir"`
	MusicDir   string `json:"music_dir"`
	Port       int    `json:"port"`
	TZ         string `json:"tz"`
	PUID       int    `json:"puid"`
	PGID       int    `json:"pgid"`
	Discovery  bool   `json:"discovery"`
	NoAVX2     bool   `json:"no_avx2"`
	VPN        bool   `json:"vpn"`
	Telemetry  bool   `json:"telemetry"`
	AdminEmail string `json:"admin_email,omitempty"`
	// SkipAdminSeed leaves SONGSTRESS_ADMIN_EMAIL/PASSWORD empty so the server
	// generates internal credentials and the first client to connect gets a
	// create-admin setup screen (Songstress is invite-only, no signup).
	SkipAdminSeed bool `json:"skip_admin_seed,omitempty"`
	// SMTP connection details (non-secret) for outbound email — invites,
	// password resets, and notifications. The password is a Secret and never
	// lands here, so these stay lock-file-safe. An empty SMTPHost means email is
	// not configured and no SMTP block is written (compose defaults cover it).
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUsername string `json:"smtp_username,omitempty"`
	SMTPFrom     string `json:"smtp_from,omitempty"`
	SMTPTo       string `json:"smtp_to,omitempty"`
	SMTPStartTLS bool   `json:"smtp_starttls,omitempty"`
	// PublicURL is SONGSTRESS_PUBLIC_URL — the origin a proxy/tunnel serves the
	// app on, for emailed links. Operators usually add it to .env by hand; a
	// re-run carries it over instead of blanking it.
	PublicURL string `json:"public_url,omitempty"`
	// NavidromeURL set = EXISTING mode: Songstress uses a Navidrome the
	// operator already runs (no navidrome service, SONGSTRESS_MANAGED_NAVIDROME=0).
	// It is the URL as the songstress CONTAINER reaches it; a localhost URL is
	// rewritten to host.docker.internal (NavidromeHostGateway) before it lands
	// here. Empty = FRESH mode: the stack provisions its own Navidrome.
	NavidromeURL      string `json:"navidrome_url,omitempty"`
	NavidromeUsername string `json:"navidrome_username,omitempty"`
	// NavidromeHostGateway maps host.docker.internal for the containers that
	// talk to Navidrome (Linux needs the explicit host-gateway entry).
	NavidromeHostGateway bool `json:"navidrome_host_gateway,omitempty"`
	// NavidromePublicURL is where players reach the existing Navidrome. Only
	// Songstress v0.31 and older use it (they redirect streams there).
	NavidromePublicURL string `json:"navidrome_public_url,omitempty"`
	// Keep carries hand-edited, NON-secret .env values across a re-run (a VPN
	// provider, LAN_SUBNET, …) so regenerating the file doesn't reset them.
	// Never persisted: the lock holds answers, not .env contents.
	Keep map[string]string `json:"-"`
}

// KeepKeys are the hand-editable, non-secret .env keys a re-run carries over.
var KeepKeys = []string{"VPN_SERVICE_PROVIDER", "WIREGUARD_ADDRESSES", "LAN_SUBNET", "NAVIDROME_PORT"}

// ManagedNavidrome reports FRESH mode: the stack runs its own Navidrome.
func (a Answers) ManagedNavidrome() bool { return a.NavidromeURL == "" }

// Secrets are generated (or user-supplied for VPN/SMTP) at install time and
// written ONLY to .env. A re-run reads them back from the existing .env and
// keeps them: Navidrome applies its admin password once, on its first boot, and
// the app admin is created once, so a rotated value would only lock them out.
type Secrets struct {
	NavidromePassword string
	AdminPassword     string
	AudioMuseToken    string
	AudioMusePassword string
	AudioMuseDB       string
	WGPrivateKey      string
	SMTPPassword      string
	// Extra is what the operator added to .env by hand (LOG_LEVEL,
	// GLUETUN_API_KEY, …), written back unchanged by a re-run. It can hold
	// credentials, so it travels with the secrets and never reaches compose.
	Extra map[string]string
}

// ProviderKeys are the acquisition providers' credentials Songstress v0.32.0+
// reads. The image carries working values; a non-empty one in .env overrides
// it. They live in .env as the operator's own settings (Secrets.Extra) and are
// never embedded here.
var ProviderKeys = []string{
	"SONGSTRESS_ANTRA_API_KEY",
	"SONGSTRESS_COMMUNITY_API_KEY",
	"SONGSTRESS_QOBUZ_APP_ID",
	"SONGSTRESS_QOBUZ_APP_SECRET",
}

// Hidden stands in for every secret in a rendering meant to be shown.
const Hidden = "<hidden>"

// Redacted is s with every value replaced by Hidden (an empty one stays
// empty, so Generate validates it the same way): what a dry run renders, so
// it shows the files without printing a password or key — on a re-run those
// are the live ones.
func (s Secrets) Redacted() Secrets {
	hide := func(v string) string {
		if v == "" {
			return ""
		}
		return Hidden
	}
	r := Secrets{
		NavidromePassword: hide(s.NavidromePassword),
		AdminPassword:     hide(s.AdminPassword),
		AudioMuseToken:    hide(s.AudioMuseToken),
		AudioMusePassword: hide(s.AudioMusePassword),
		AudioMuseDB:       hide(s.AudioMuseDB),
		WGPrivateKey:      hide(s.WGPrivateKey),
		SMTPPassword:      hide(s.SMTPPassword),
	}
	if s.Extra != nil {
		r.Extra = make(map[string]string, len(s.Extra))
		for key, value := range s.Extra {
			r.Extra[key] = hide(value)
		}
	}
	return r
}
