// Package execute shells out to docker compose and polls health — the
// "bring it up" half of install. All exec goes through preflight.Runner so
// the sequencing is unit-testable.
package execute

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/preflight"
)

// absArgs anchors `-f <file>` arguments to dir — docker compose resolves -f
// against the process cwd, not --project-directory.
func absArgs(dir string, composeArgs []string) []string {
	out := make([]string, len(composeArgs))
	for i, a := range composeArgs {
		if strings.HasPrefix(a, "-") || filepath.IsAbs(a) {
			out[i] = a
		} else {
			out[i] = filepath.Join(dir, a)
		}
	}
	return out
}

// Up pulls images then starts the stack. Progress lines go through report
// (nil = silent); the caller wraps steps in spinners when interactive.
func Up(dir string, composeArgs []string, run preflight.Runner, report func(string)) error {
	say := func(s string) {
		if report != nil {
			report(s)
		}
	}
	base := append([]string{"compose", "--project-directory", dir}, absArgs(dir, composeArgs)...)

	say("Pulling images (first run downloads several GB)…")
	// --ignore-pull-failures: locally built override images (e.g. --songstress-image
	// during testing) aren't in any registry. `up -d` below is the integrity
	// gate — it fails loudly if an image is truly missing.
	if out, err := run("docker", append(base, "pull", "--ignore-pull-failures")...); err != nil {
		return fmt.Errorf("docker compose pull failed: %w\n%s", err, out)
	}
	say("Starting services…")
	if out, err := run("docker", append(base, "up", "-d", "--quiet-pull")...); err != nil {
		return fmt.Errorf("docker compose up failed: %w\n%s", err, out)
	}
	return nil
}

// PollHTTP waits for a 2xx from url, retrying `attempts` times with `delay`
// between tries.
func PollHTTP(url string, attempts int, delay time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for i := 0; i < attempts; i++ {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(delay)
	}
	return fmt.Errorf("no healthy response from %s: %w", url, lastErr)
}

// PS returns `docker compose ps` output for the success report.
func PS(dir string, composeArgs []string, run preflight.Runner) string {
	args := append([]string{"compose", "--project-directory", dir}, absArgs(dir, composeArgs)...)
	out, _ := run("docker", append(args, "ps", "--format", "table {{.Name}}\t{{.Status}}")...)
	return out
}
