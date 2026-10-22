package execute

import (
	"strings"
	"testing"
)

func TestUpRunsPullThenUp(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	if err := Up("/tmp/x", []string{"-f", "compose.yaml"}, run, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %v", calls)
	}
	if !strings.Contains(calls[0], "pull") || !strings.Contains(calls[0], "--project-directory /tmp/x") {
		t.Fatalf("bad pull call: %s", calls[0])
	}
	if !strings.Contains(calls[0], "-f /tmp/x/compose.yaml") {
		t.Fatalf("compose file not anchored to project dir: %s", calls[0])
	}
	if !strings.Contains(calls[1], "up -d") {
		t.Fatalf("bad up call: %s", calls[1])
	}
}

// A pinned image that doesn't exist must fail at pull, loudly — never be
// skipped over (the v0.21.0 pin that was never published did exactly that).
func TestPullFailuresAreFatalForRegistryImages(t *testing.T) {
	var pull string
	run := func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), " pull") {
			pull = strings.Join(args, " ")
		}
		return "", nil
	}
	if err := Up("/tmp/x", []string{"-f", "compose.yaml"}, run, nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pull, "--ignore-pull-failures") {
		t.Fatalf("registry pins must pull strictly: %s", pull)
	}
	if err := Up("/tmp/x", []string{"-f", "compose.yaml"}, run, nil, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pull, "--ignore-pull-failures") {
		t.Fatalf("a local override image is in no registry: %s", pull)
	}
}

func TestUpSurfacesPullFailure(t *testing.T) {
	run := func(name string, args ...string) (string, error) {
		return "manifest unknown", errFake
	}
	err := Up("/tmp/x", []string{"-f", "compose.yaml"}, run, nil, false)
	if err == nil || !strings.Contains(err.Error(), "pull failed") || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("expected pull failure, got %v", err)
	}
}

type fakeErr struct{}

func (fakeErr) Error() string { return "boom" }

var errFake = fakeErr{}
