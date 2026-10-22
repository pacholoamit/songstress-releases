// Package execute shells out to docker compose — the "bring it up" half of
// install. All exec goes through preflight.Runner so the sequencing is
// unit-testable. Whether the result actually WORKS is internal/verify's job.
package execute

import (
	"fmt"
	"path/filepath"
	"strings"

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
//
// A failed pull stops the install with the registry's own error: a pinned tag
// that doesn't exist must say so. (Pulling with --ignore-pull-failures hid
// exactly that — a manifest pinned an unpublished image and every install died
// later in `up` with a confusing message.) localImage tolerates pull failures
// for a locally built override image (--songstress-image when testing), which
// no registry has; `up -d` is then the integrity gate.
func Up(dir string, composeArgs []string, run preflight.Runner, report func(string), localImage bool) error {
	say := func(s string) {
		if report != nil {
			report(s)
		}
	}
	base := append([]string{"compose", "--project-directory", dir}, absArgs(dir, composeArgs)...)

	say("Pulling images (first run downloads several GB)…")
	pull := append(base, "pull")
	if localImage {
		pull = append(pull, "--ignore-pull-failures")
	}
	if out, err := run("docker", pull...); err != nil {
		return fmt.Errorf("docker compose pull failed: %w\n%s", err, out)
	}
	say("Starting services…")
	if out, err := run("docker", append(base, "up", "-d", "--quiet-pull")...); err != nil {
		return fmt.Errorf("docker compose up failed: %w\n%s", err, out)
	}
	return nil
}

// PS returns `docker compose ps` output for the success report.
func PS(dir string, composeArgs []string, run preflight.Runner) string {
	args := append([]string{"compose", "--project-directory", dir}, absArgs(dir, composeArgs)...)
	out, _ := run("docker", append(args, "ps", "--format", "table {{.Name}}\t{{.Status}}")...)
	return out
}
