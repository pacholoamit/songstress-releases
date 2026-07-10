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
	HTTPS      bool   `json:"https"`
	Domain     string `json:"domain,omitempty"`
	ACMEEmail  string `json:"acme_email,omitempty"`
	Tailscale  bool   `json:"tailscale"`
	Telemetry  bool   `json:"telemetry"`
	AdminEmail string `json:"admin_email,omitempty"`
}

// Secrets are generated (or user-supplied for VPN/Tailscale keys) at install
// time and written ONLY to .env.
type Secrets struct {
	NavidromePassword string
	AdminPassword     string
	AudioMuseToken    string
	AudioMusePassword string
	AudioMuseDB       string
	WGPrivateKey      string
	TSAuthKey         string
}
