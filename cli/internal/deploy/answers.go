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
}

// Secrets are generated (or user-supplied for VPN/SMTP) at install time and
// written ONLY to .env.
type Secrets struct {
	NavidromePassword string
	AdminPassword     string
	AudioMuseToken    string
	AudioMusePassword string
	AudioMuseDB       string
	WGPrivateKey      string
	SMTPPassword      string
}
