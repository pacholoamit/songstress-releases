package deploy

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"text/template"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

// tmplCtx is what compose/Caddyfile templates see — deliberately WITHOUT
// Secrets, so a template mistake can't embed a credential outside .env.
type tmplCtx struct {
	Answers
	Pins       map[string]string
	CLIVersion string
}

// envCtx is the one context that carries secrets; only env.tmpl gets it.
type envCtx struct {
	tmplCtx
	S Secrets
}

// Rendered is the complete on-disk deployment the install command writes.
type Rendered struct {
	Files       map[string][]byte
	ComposeArgs []string
}

// Generate renders the deployment for the chosen components. It validates
// cross-field requirements and never touches the filesystem.
func Generate(a Answers, s Secrets, m Manifest) (Rendered, error) {
	if a.MusicDir == "" {
		return Rendered{}, fmt.Errorf("music dir is required")
	}
	if a.HTTPS && (a.Domain == "" || a.ACMEEmail == "") {
		return Rendered{}, fmt.Errorf("https requires a domain and an acme email")
	}
	if a.VPN && s.WGPrivateKey == "" {
		return Rendered{}, fmt.Errorf("vpn requires a wireguard private key")
	}
	if a.Tailscale && s.TSAuthKey == "" {
		return Rendered{}, fmt.Errorf("tailscale requires an auth key")
	}

	pins := map[string]string{}
	for k, v := range m.Images {
		pins[k] = v
	}
	if a.NoAVX2 {
		pins["audiomuse"] = pins["audiomuse_noavx2"]
	}

	ctx := tmplCtx{Answers: a, Pins: pins, CLIVersion: m.CLI}
	out := Rendered{Files: map[string][]byte{}, ComposeArgs: []string{"-f", "compose.yaml"}}

	render := func(name, target string, data any) error {
		t, err := template.ParseFS(tmplFS, "templates/"+name)
		if err != nil {
			return err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return err
		}
		out.Files[target] = b.Bytes()
		return nil
	}
	overlay := func(name, target string) error {
		if err := render(name, target, ctx); err != nil {
			return err
		}
		out.ComposeArgs = append(out.ComposeArgs, "-f", target)
		return nil
	}

	if err := render("compose.yaml.tmpl", "compose.yaml", ctx); err != nil {
		return Rendered{}, err
	}
	if a.Discovery {
		if err := overlay("compose.discovery.yaml.tmpl", "compose.discovery.yaml"); err != nil {
			return Rendered{}, err
		}
	}
	if a.VPN {
		if err := overlay("compose.vpn.yaml.tmpl", "compose.vpn.yaml"); err != nil {
			return Rendered{}, err
		}
	}
	if a.HTTPS {
		if err := overlay("compose.https.yaml.tmpl", "compose.https.yaml"); err != nil {
			return Rendered{}, err
		}
		if err := render("Caddyfile.tmpl", "Caddyfile", ctx); err != nil {
			return Rendered{}, err
		}
	}
	if a.Tailscale {
		if err := overlay("compose.tailscale.yaml.tmpl", "compose.tailscale.yaml"); err != nil {
			return Rendered{}, err
		}
		serve, err := tailscaleServeJSON(a)
		if err != nil {
			return Rendered{}, err
		}
		out.Files["tailscale-serve.json"] = serve
	}
	if err := render("env.tmpl", ".env", envCtx{tmplCtx: ctx, S: s}); err != nil {
		return Rendered{}, err
	}
	return out, nil
}

// tailscaleServeJSON proxies the tailnet HTTPS listener to the dashboard.
func tailscaleServeJSON(a Answers) ([]byte, error) {
	target := "songstress"
	if a.VPN {
		target = "gluetun"
	}
	cfg := map[string]any{
		"TCP": map[string]any{"443": map[string]any{"HTTPS": true}},
		"Web": map[string]any{
			"${TS_CERT_DOMAIN}:443": map[string]any{
				"Handlers": map[string]any{
					"/": map[string]any{"Proxy": fmt.Sprintf("http://%s:8090", target)},
				},
			},
		},
		"AllowFunnel": map[string]any{"${TS_CERT_DOMAIN}:443": false},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
