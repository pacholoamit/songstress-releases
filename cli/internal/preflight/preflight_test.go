package preflight

import (
	"errors"
	"strings"
	"testing"
)

func fake(responses map[string]string, fails map[string]bool) Runner {
	return func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		if fails[key] {
			return "", errors.New("exec failed: " + key)
		}
		return responses[key], nil
	}
}

func TestDockerAndComposeDetected(t *testing.T) {
	r, err := Run(fake(map[string]string{
		"docker version --format {{.Server.Version}}": "29.6.1",
		"docker compose version --short":              "2.39.1",
	}, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.DockerOK || !r.ComposeOK || r.ComposeVersion != "2.39.1" {
		t.Fatalf("bad result: %+v", r)
	}
}

func TestMissingDaemonIsNotFatal(t *testing.T) {
	r, err := Run(fake(nil, map[string]bool{
		"docker version --format {{.Server.Version}}": true,
		"docker compose version --short":              true,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.DockerOK || r.ComposeOK {
		t.Fatalf("expected docker/compose unavailable: %+v", r)
	}
}

func TestPortProbe(t *testing.T) {
	r, err := Run(fake(map[string]string{
		"docker version --format {{.Server.Version}}": "x",
		"docker compose version --short":              "2",
	}, nil), []int{54321})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PortFree[54321] {
		t.Fatal("expected high port free")
	}
}

func TestHostDetectionPopulated(t *testing.T) {
	r, err := Run(fake(nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Host.OS == "" || r.Host.Arch == "" {
		t.Fatalf("host not detected: %+v", r.Host)
	}
}
