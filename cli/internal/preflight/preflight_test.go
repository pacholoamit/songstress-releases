package preflight

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
)

const (
	versionCmd = "docker version --format {{.Server.Version}}"
	composeCmd = "docker compose version --short"
	infoCmd    = "docker info --format {{.MemTotal}}|{{.DockerRootDir}}|{{.OperatingSystem}}"
)

// fake answers each command line with its response; a command in fails exits
// non-zero with that output.
func fake(responses map[string]string, fails map[string]string) Runner {
	return func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		if out, ok := fails[key]; ok {
			return out, errors.New("exit status 1")
		}
		return responses[key], nil
	}
}

func TestDockerAndComposeDetected(t *testing.T) {
	r, err := Run(fake(map[string]string{versionCmd: "29.6.1", composeCmd: "2.39.1"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !r.DockerOK || !r.ComposeOK || r.ComposeVersion != "2.39.1" || r.Problem != nil {
		t.Fatalf("bad result: %+v", r)
	}
}

// "Docker is required" helped nobody whose Docker was merely stopped, or
// whose user wasn't allowed to use it: each failure names its own fix.
func TestDockerProblemsSayWhatToDo(t *testing.T) {
	cases := map[string]struct {
		run  Runner
		want string
	}{
		"not installed": {func(string, ...string) (string, error) {
			return "", &exec.Error{Name: "docker", Err: exec.ErrNotFound}
		}, "Docker isn't installed"},
		"not running": {fake(nil, map[string]string{versionCmd: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}),
			"not running — start it"},
		"no permission": {fake(nil, map[string]string{versionCmd: "permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock"}),
			"usermod -aG docker"},
		"no compose plugin": {fake(map[string]string{versionCmd: "29.6.1"}, map[string]string{composeCmd: "docker: 'compose' is not a docker command."}),
			"compose plugin is missing"},
		"anything else": {fake(nil, map[string]string{versionCmd: "context \"remote\" does not exist"}),
			`Docker isn't working: context "remote" does not exist`},
	}
	for name, c := range cases {
		r, err := Run(c.run)
		if err != nil {
			t.Fatal(err)
		}
		if r.Problem == nil || !strings.Contains(r.Problem.Error(), c.want) {
			t.Errorf("%s: problem %v, want it to say %q", name, r.Problem, c.want)
		}
	}
}

// Discovery's default follows the memory Docker can give containers: on Docker
// Desktop that's the VM's, not the Mac's.
func TestMemoryComesFromDocker(t *testing.T) {
	r, err := Run(fake(map[string]string{versionCmd: "29.6.1", composeCmd: "2.39.1",
		infoCmd: "4106080256|/var/lib/docker|Docker Desktop"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.DockerMemGB != 4 || r.MemoryGB() != 4 {
		t.Fatalf("Docker's 3.8 GiB should read as 4 GB: %+v", r)
	}
	if !hasNote(r, "Docker Desktop gives containers 4 GB") || !hasNote(r, "Settings → Resources") {
		t.Fatalf("expected a Docker Desktop memory note: %q", r.Notes)
	}
	r, _ = Run(fake(map[string]string{versionCmd: "29.6.1", composeCmd: "2.39.1",
		infoCmd: "33494827008|/var/lib/docker|Ubuntu 24.04 LTS"}, nil))
	if r.MemoryGB() != 31 || hasNote(r, "Discovery") {
		t.Fatalf("31 GB is plenty for Discovery: %+v", r)
	}
}

func TestOldDockerIsNoted(t *testing.T) {
	r, _ := Run(fake(map[string]string{versionCmd: "19.03.15", composeCmd: "2.20.0"}, nil))
	if !hasNote(r, "older than 20.10") {
		t.Fatalf("expected an old-Docker note: %q", r.Notes)
	}
	r, _ = Run(fake(map[string]string{versionCmd: "20.10.24+dfsg1", composeCmd: "2.20.0"}, nil))
	if hasNote(r, "older than") {
		t.Fatalf("20.10 is new enough: %q", r.Notes)
	}
}

func TestMajorMinor(t *testing.T) {
	for in, want := range map[string][2]int{"29.6.1": {29, 6}, "20.10.24+dfsg1": {20, 10}, "v2.39.1": {2, 39}} {
		major, minor, ok := majorMinor(in)
		if !ok || major != want[0] || minor != want[1] {
			t.Errorf("%s → %d.%d %v", in, major, minor, ok)
		}
	}
	if _, _, ok := majorMinor("dev"); ok {
		t.Error("dev is no version")
	}
}

// Docker publishes on the IPv4 wildcard; the probe must see a port taken that
// way (which a 127.0.0.1 or dual-stack probe misses on macOS), and one taken
// any other way.
func TestPortFree(t *testing.T) {
	for _, l := range [][2]string{{"tcp4", "0.0.0.0:0"}, {"tcp4", "127.0.0.1:0"}, {"tcp", ":0"}} {
		ln, err := net.Listen(l[0], l[1])
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if PortFree(port) {
			t.Errorf("port %d is taken (%s %s)", port, l[0], l[1])
		}
		ln.Close()
		if !PortFree(port) {
			t.Errorf("port %d was released", port)
		}
	}
}

func TestFreeGB(t *testing.T) {
	if _, ok := freeGB(t.TempDir()); !ok {
		t.Fatal("a temp dir's filesystem has a free-space figure")
	}
	if _, ok := freeGB(fmt.Sprintf("/nonexistent-%d/docker", 42)); ok {
		t.Fatal("a path on no local filesystem has none")
	}
}

func TestHostDetectionPopulated(t *testing.T) {
	r, err := Run(fake(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.Host.OS == "" || r.Host.Arch == "" {
		t.Fatalf("host not detected: %+v", r.Host)
	}
}

func hasNote(r Result, part string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}
