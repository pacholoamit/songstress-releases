package deploy

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

// tmplCtx is what the compose templates see — deliberately WITHOUT Secrets, so
// a template mistake can't embed a credential outside .env.
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
//
// Access networking is the operator's own: the stack publishes the dashboard
// port on the host and nothing here terminates TLS or joins a tailnet.
func Generate(a Answers, s Secrets, m Manifest) (Rendered, error) {
	if a.MusicDir == "" {
		return Rendered{}, fmt.Errorf("music dir is required")
	}
	if a.VPN && s.WGPrivateKey == "" {
		return Rendered{}, fmt.Errorf("vpn requires a wireguard private key")
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
	if err := render("env.tmpl", ".env", envCtx{tmplCtx: ctx, S: s}); err != nil {
		return Rendered{}, err
	}
	return out, nil
}
