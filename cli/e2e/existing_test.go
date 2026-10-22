//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// TestExistingNavidrome points the installer at a Navidrome that's already
// running: a wrong password and a non-admin account are refused before
// anything is written, and an admin account installs a stack with no Navidrome
// of its own that signs in to this one, scans and streams.
func TestExistingNavidrome(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	cli := cliBinary(t)
	m, err := deploy.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	image := m.Images["songstress"]
	if v := os.Getenv("SONGSTRESS_IMAGE"); v != "" {
		image = v
	}
	port := os.Getenv("SONGSTRESS_E2E_PORT")
	if port == "" {
		port = "18090"
	}
	clock := time.Now()
	lap := func(what string) {
		t.Logf("⏱ %-38s %6.1fs", what, time.Since(clock).Seconds())
		clock = time.Now()
	}

	pull(t, image)
	music := fixtureLibrary(t, image)
	ndURL := standaloneNavidrome(t, m.Images["navidrome"], music)
	addUser(t, ndURL, "bob", "bob-pass-1", false)
	lap("standalone Navidrome with an admin and a member")

	dir := filepath.Join(t.TempDir(), "songstress")
	base := []string{"install", "--yes", "--music-dir", music, "--install-dir", dir, "--port", port,
		"--telemetry=false", "--navidrome-url", ndURL}
	if os.Getenv("SONGSTRESS_IMAGE") != "" {
		base = append(base, "--songstress-image", image)
	}
	refused := func(user, pass, want string) {
		t.Helper()
		cmd := exec.Command(cli, append(base, "--navidrome-username", user)...)
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "SONGSTRESS_NAVIDROME_PASSWORD="+pass)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("expected refusal %q, got err=%v\n%s", want, err, out)
		}
		if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
			t.Fatal("a refused Navidrome must leave nothing written")
		}
	}
	refused("admin", "wrong-password", "rejected the username or password")
	refused("bob", "bob-pass-1", "is not an admin")
	lap("bad login and non-admin refused, nothing written")

	t.Cleanup(func() { teardown(t, dir) })
	cmd := exec.Command(cli, append(base, "--navidrome-username", "admin")...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "SONGSTRESS_NAVIDROME_PASSWORD="+ndAdminPassword)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	lap("songstress install (EXISTING, verified)")

	compose, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(compose), "\n  navidrome:") || !strings.Contains(string(compose), "SONGSTRESS_MANAGED_NAVIDROME=0") {
		t.Fatal("an existing Navidrome must not get a second one from the stack")
	}
	env := readEnv(t, dir)
	pb := &client{t: t, base: "http://127.0.0.1:" + port}
	pb.signIn(env["SONGSTRESS_ADMIN_EMAIL"], env["SONGSTRESS_ADMIN_PASSWORD"])
	pb.expectHeartbeat()
	pb.expectNavidromeVerified()
	songs := pb.waitForSongs(3)
	pb.expectRangeStream(songs[0])
	lap("sign-in, scan and Range/206 through it")
}

const ndAdminPassword = "nd-admin-pass-1"

// standaloneNavidrome runs Navidrome the way an operator already would, on a
// free host port; returns its URL as seen from this host (localhost).
func standaloneNavidrome(t *testing.T, image, music string) string {
	t.Helper()
	port := freePort(t)
	name := fmt.Sprintf("songstress-e2e-existing-nd-%d", port)
	data := filepath.Join(t.TempDir(), "nd-data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, "docker", "run", "-d", "--name", name,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-p", fmt.Sprintf("%d:4533", port),
		"-e", "ND_DEVAUTOCREATEADMINPASSWORD="+ndAdminPassword,
		"-e", "ND_SCANNER_SCHEDULE=0", "-e", "ND_SCANNER_SCANONSTARTUP=true",
		"-v", data+":/data", "-v", music+":/music:ro", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	url := fmt.Sprintf("http://localhost:%d", port)
	deadline := time.Now().Add(time.Minute)
	for {
		if resp, err := http.Get(url + "/ping"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("standalone Navidrome never answered /ping")
		}
		time.Sleep(time.Second)
	}
}

// addUser creates a Navidrome account through its native API (the test's own
// throwaway server, so the admin's plaintext login is fine here).
func addUser(t *testing.T, ndURL, user, pass string, admin bool) {
	t.Helper()
	post := func(path, token string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, ndURL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("x-nd-authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("/auth/login", "", map[string]string{"username": "admin", "password": ndAdminPassword})
	var login struct{ Token string }
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil || login.Token == "" {
		t.Fatalf("navidrome admin login: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = post("/api/user", login.Token, map[string]any{"userName": user, "name": user, "password": pass, "isAdmin": admin})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create navidrome user %s: HTTP %d", user, resp.StatusCode)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
