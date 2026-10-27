package deploy

import "strings"

// quoteEnv renders a .env value so docker compose reads back exactly these
// bytes. Unquoted, compose expands `$`, cuts at ` #`, and trims — so a chosen
// password like `pa$$ #1` would reach the container as something else (and an
// existing Navidrome's password would then fail the sign-in the installer just
// verified). Safe values stay bare; anything else is single-quoted (literal: no
// expansion, no escapes); a value holding a single quote is double-quoted with
// `\` and `"` escaped and `$` doubled — compose's own escape rules.
func quoteEnv(v string) string {
	if v == "" || bareSafe(v) {
		return v
	}
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`).Replace(v) + `"`
}

func bareSafe(v string) bool {
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_./:@,+=%-", r):
		default:
			return false
		}
	}
	return true
}

// unquoteEnv is quoteEnv's inverse for the forms it writes (and plain
// hand-edited values).
func unquoteEnv(v string) string {
	n := len(v)
	switch {
	case n >= 2 && v[0] == '\'' && v[n-1] == '\'':
		return v[1 : n-1]
	case n >= 2 && v[0] == '"' && v[n-1] == '"':
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `$$`, `$`).Replace(v[1 : n-1])
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
