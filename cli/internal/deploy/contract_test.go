package deploy

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The server repo's compose.yaml decides what the songstress service is
// handed. Its env-contract test (apps/worker/tests/test_env_contract.py) keeps
// every variable the server reads forwarded there and every forwarded one read,
// and lists what the image sets for itself (INTERNAL). serverEnvSnapshot is that
// contract as of the last sync. For a current server (v0.32+) the installer's
// compose forwards the same names, with the server's own value wherever the
// installer doesn't set one itself. After a server release, re-sync with
//
//	SONGSTRESS_SERVER_REPO=<server checkout> go test -count=1 ./internal/deploy -run TestServerEnvSnapshot -update
//
// review the snapshot's diff, and bring compose.yaml.tmpl in step.
const serverEnvSnapshot = "testdata/server-env.txt"

// installerValues are the variables the installer sets its own way: from the
// .env it writes from your answers, or fixed for the stack it generates.
var installerValues = map[string]bool{
	"TZ": true, "PUID": true, "PGID": true,
	"SONGSTRESS_ADMIN_EMAIL": true, "SONGSTRESS_ADMIN_PASSWORD": true, "ENABLE_TELEMETRY": true,
	"MUSIC_LIBRARIES": true, "SONGSTRESS_MANAGED_NAVIDROME": true,
	"NAVIDROME_URL": true, "NAVIDROME_USERNAME": true, "NAVIDROME_PASSWORD": true,
	"AUDIOMUSE_URL": true, "AUDIOMUSE_API_TOKEN": true,
}

type envEntry struct{ name, value string }

type serverContract struct {
	forwarded []envEntry
	internal  []string
}

var (
	composeEntry  = regexp.MustCompile(`^\s+- ([A-Z][A-Z0-9_]*)=(.*)$`)
	serviceEnd    = regexp.MustCompile(`^ {0,2}\S`)
	internalEntry = regexp.MustCompile(`^\s+"([A-Z][A-Z0-9_]*)":`)
)

// serviceEnv is the `- NAME=value` environment of one service in a compose
// file, in order; the service's block ends at the next line indented two
// spaces or less (the server's test reads compose.yaml the same way).
func serviceEnv(compose []byte, service string) []envEntry {
	lines := strings.Split(string(compose), "\n")
	start := slices.Index(lines, "  "+service+":")
	if start < 0 {
		return nil
	}
	var env []envEntry
	for _, line := range lines[start+1:] {
		if serviceEnd.MatchString(line) {
			break
		}
		if m := composeEntry.FindStringSubmatch(line); m != nil {
			value, _, _ := strings.Cut(m[2], " #") // a YAML comment after the item
			env = append(env, envEntry{m[1], strings.TrimSpace(value)})
		}
	}
	return env
}

// internalNames are the keys of the server test's INTERNAL dict.
func internalNames(contractTest []byte) []string {
	var names []string
	inside := false
	for _, line := range strings.Split(string(contractTest), "\n") {
		switch {
		case strings.HasPrefix(line, "INTERNAL = {"):
			inside = true
		case inside && strings.HasPrefix(line, "}"):
			return names
		case inside:
			if m := internalEntry.FindStringSubmatch(line); m != nil {
				names = append(names, m[1])
			}
		}
	}
	return names
}

func (c serverContract) render() string {
	var b strings.Builder
	b.WriteString("# The server's env contract: the songstress service's environment in its\n" +
		"# compose.yaml (forward), and what its env-contract test lists as set by the\n" +
		"# image (internal). Refresh from a server checkout, then review the diff:\n" +
		"#   SONGSTRESS_SERVER_REPO=<checkout> go test -count=1 ./internal/deploy -run TestServerEnvSnapshot -update\n")
	for _, e := range c.forwarded {
		fmt.Fprintf(&b, "forward %s=%s\n", e.name, e.value)
	}
	for _, name := range c.internal {
		fmt.Fprintf(&b, "internal %s\n", name)
	}
	return b.String()
}

func loadContract(t *testing.T) serverContract {
	t.Helper()
	b, err := os.ReadFile(serverEnvSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	var c serverContract
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		kind, rest, _ := strings.Cut(sc.Text(), " ")
		switch kind {
		case "forward":
			name, value, _ := strings.Cut(rest, "=")
			c.forwarded = append(c.forwarded, envEntry{name, value})
		case "internal":
			c.internal = append(c.internal, rest)
		}
	}
	if len(c.forwarded) == 0 {
		t.Fatalf("%s lists nothing", serverEnvSnapshot)
	}
	return c
}

// TestServerEnvSnapshot compares the snapshot with a server checkout (or, with
// -update, rewrites it from one). It needs SONGSTRESS_SERVER_REPO.
func TestServerEnvSnapshot(t *testing.T) {
	repo := os.Getenv("SONGSTRESS_SERVER_REPO")
	if repo == "" {
		t.Skip("set SONGSTRESS_SERVER_REPO to a server checkout to compare the env-contract snapshot with it")
	}
	compose, err := os.ReadFile(filepath.Join(repo, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contractTest, err := os.ReadFile(filepath.Join(repo, "apps", "worker", "tests", "test_env_contract.py"))
	if err != nil {
		t.Fatal(err)
	}
	live := serverContract{forwarded: serviceEnv(compose, "songstress"), internal: internalNames(contractTest)}.render()
	if *update {
		if err := os.WriteFile(serverEnvSnapshot, []byte(live), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want, _ := os.ReadFile(serverEnvSnapshot); string(want) != live {
		t.Fatalf("the server's env contract changed since the last sync:\n%s"+
			"rerun with -update, review the diff, bring compose.yaml.tmpl in step", lineDiff(string(want), live))
	}
}

// lineDiff lists the lines only in old ("-") and only in new ("+").
func lineDiff(old, new string) string {
	var b strings.Builder
	oldLines, newLines := strings.Split(old, "\n"), strings.Split(new, "\n")
	for _, l := range oldLines {
		if !slices.Contains(newLines, l) {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range newLines {
		if !slices.Contains(oldLines, l) {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// A current server gets exactly the server's contract: every name its
// compose.yaml forwards, with its value unless the installer sets its own, and
// nothing the image sets for itself.
func TestComposeForwardsTheServerEnvContract(t *testing.T) {
	c := loadContract(t)
	modes := map[string]func(*Answers){"managed": func(*Answers) {}, "existing": existingAnswers}
	for name, mode := range modes {
		t.Run(name, func(t *testing.T) {
			r := render(t, matrixCase{pin: modernPin, mut: func(a *Answers) { a.Discovery, a.VPN = true, true; mode(a) }})
			got := map[string]string{}
			for _, e := range serviceEnv(r.Files["compose.yaml"], "songstress") {
				got[e.name] = e.value
			}
			want := map[string]bool{}
			for _, e := range c.forwarded {
				want[e.name] = true
				value, ok := got[e.name]
				switch {
				case !ok:
					t.Errorf("not forwarded: %s (the server's compose sets %s=%s)", e.name, e.name, e.value)
				case !installerValues[e.name] && value != e.value:
					t.Errorf("%s=%s, the server's compose has %s=%s", e.name, value, e.name, e.value)
				}
			}
			for n := range got {
				if !want[n] {
					t.Errorf("forwarded but not in the server's contract: %s", n)
				}
			}
			for _, n := range c.internal {
				if _, ok := got[n]; ok {
					t.Errorf("%s is set by the image itself and must stay out of compose", n)
				}
			}
		})
	}
}

// An older server keeps the environment it was verified with: none of the
// newer contract reaches it (an empty SERVICE_ORDER or DISCOGRAPHY_OUTPUT_TEMPLATE
// means something else to a v0.31 worker).
func TestLegacyServerKeepsItsOwnEnv(t *testing.T) {
	for _, name := range []string{"minimal", "everything", "existing"} {
		compose := string(render(t, matrix()[name]).Files["compose.yaml"])
		for _, newer := range []string{"SERVICE_ORDER", "DISCOGRAPHY_OUTPUT_TEMPLATE", "SONGSTRESS_ANTRA_API_KEY", "mem_limit", "x-logging"} {
			if strings.Contains(compose, newer) {
				t.Errorf("%s: a v0.31 pin renders %s", name, newer)
			}
		}
	}
}
