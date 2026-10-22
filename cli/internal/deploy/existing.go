package deploy

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Existing is what an earlier `songstress install` left in a directory. A
// re-run must build on it, never over it: Navidrome applies its admin password
// once (on its first boot) and the app admin is created once, so regenerating
// those secrets only locks the operator out — the old installer did exactly
// that, and printed the new, wrong password as the one to sign in with.
type Existing struct {
	Lock    Lock
	HasLock bool
	// Env is the previous .env (nil when there is none).
	Env map[string]string
}

// LoadExisting reads dir's lock and .env; both are optional.
func LoadExisting(dir string) (Existing, error) {
	var e Existing
	var err error
	if e.Lock, e.HasLock, err = ReadLock(dir); err != nil {
		return e, err
	}
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if errors.Is(err, fs.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.Env = ParseEnv(b)
	return e, nil
}

// Found reports whether dir already holds an install.
func (e Existing) Found() bool { return e.HasLock || e.Env != nil }

// Secrets returns the secrets the earlier install generated or was given, and
// the settings the operator added to its .env by hand.
func (e Existing) Secrets() Secrets {
	return Secrets{
		NavidromePassword: e.Env["NAVIDROME_PASSWORD"],
		AdminPassword:     e.Env["SONGSTRESS_ADMIN_PASSWORD"],
		AudioMuseToken:    e.Env["AUDIOMUSE_API_TOKEN"],
		AudioMusePassword: e.Env["AUDIOMUSE_PASSWORD"],
		AudioMuseDB:       e.Env["AUDIOMUSE_DB_PASSWORD"],
		WGPrivateKey:      e.Env["WIREGUARD_PRIVATE_KEY"],
		SMTPPassword:      e.Env["SMTP_PASSWORD"],
		Extra:             e.extras(),
	}
}

// retiredKeys were written by earlier releases of this CLI for features it no
// longer generates (Tailscale); a re-run drops them rather than keeping them as
// the operator's own.
var retiredKeys = map[string]bool{"TS_AUTHKEY": true}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// templateKeys are the keys env.tmpl writes, in any mode. Every other key in a
// previous .env was added by hand.
var templateKeys = sync.OnceValue(func() map[string]bool {
	b, _ := tmplFS.ReadFile("templates/env.tmpl") // embedded: can't fail
	keys := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if key, _, ok := strings.Cut(line, "="); ok && envName.MatchString(key) {
			keys[key] = true
		}
	}
	return keys
})

// extras are the settings in the previous .env that the template doesn't
// write: the operator's own (nil when there are none).
func (e Existing) extras() map[string]string {
	var out map[string]string
	for key, value := range e.Env {
		if templateKeys()[key] || retiredKeys[key] || !envName.MatchString(key) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = value
	}
	return out
}

// Keep returns the hand-editable, non-secret .env values to carry over.
func (e Existing) Keep() map[string]string {
	keep := map[string]string{}
	for _, k := range KeepKeys {
		if v := e.Env[k]; v != "" {
			keep[k] = v
		}
	}
	return keep
}

// MergeSecrets keeps every secret the earlier install already has, so a
// re-run changes nothing a running stack depends on. `given` wins only where a
// new value is legitimately the operator's to change: a WireGuard key, the
// SMTP password, and — for an EXISTING Navidrome — that server's password
// (the installer verifies it before writing). The managed Navidrome's
// password, the app admin's, and AudioMuse's (its database was initialised
// with them) never change once set. The operator's own .env lines are only
// ever carried over.
func MergeSecrets(prev, given Secrets, existingNavidrome bool) Secrets {
	keep := func(old, new string) string {
		if old != "" {
			return old
		}
		return new
	}
	replace := func(old, new string) string {
		if new != "" {
			return new
		}
		return old
	}
	nd := keep(prev.NavidromePassword, given.NavidromePassword)
	if existingNavidrome {
		nd = replace(prev.NavidromePassword, given.NavidromePassword)
	}
	return Secrets{
		NavidromePassword: nd,
		AdminPassword:     keep(prev.AdminPassword, given.AdminPassword),
		AudioMuseToken:    keep(prev.AudioMuseToken, given.AudioMuseToken),
		AudioMusePassword: keep(prev.AudioMusePassword, given.AudioMusePassword),
		AudioMuseDB:       keep(prev.AudioMuseDB, given.AudioMuseDB),
		WGPrivateKey:      replace(prev.WGPrivateKey, given.WGPrivateKey),
		SMTPPassword:      replace(prev.SMTPPassword, given.SMTPPassword),
		Extra:             prev.Extra,
	}
}

// ParseEnv reads KEY=VALUE lines the way compose does for the values this
// installer writes (see quoteEnv): comments and blank lines skipped, an
// `export ` prefix tolerated, quoting undone, and a ` #` comment after an
// unquoted value dropped.
func ParseEnv(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = unquoteEnv(strings.TrimSpace(value))
	}
	return out
}
