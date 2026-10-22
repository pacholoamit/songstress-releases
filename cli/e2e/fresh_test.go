//go:build e2e

// Package e2e installs a real Songstress with the real CLI on a real Docker
// and checks it the way a listener would notice: the server answers, its worker
// reports in, it signs in to its Navidrome, the library is scanned, a track
// streams with Range/206 — and a re-run changes no password.
//
//	go test -tags e2e ./e2e -v -timeout 30m
//
// SONGSTRESS_CLI                 the binary under test (default: built from this module)
// SONGSTRESS_IMAGE               override the pinned songstress image (default: the manifest pin)
// SONGSTRESS_E2E_PORT            dashboard port (default 18090)
// SONGSTRESS_E2E_NAVIDROME_PORT  the bundled Navidrome's host port (default 4533)
// SONGSTRESS_E2E_EXISTING_ND_PORT the standalone Navidrome's host port (default: any free one)
// COMPOSE_PROJECT_NAME           the stack's compose project (default: the install dir's name)
//
// On a shared Docker host, set the ports and COMPOSE_PROJECT_NAME so nothing
// here collides with another stack.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

func TestFreshInstall(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	cli := cliBinary(t)
	image := os.Getenv("SONGSTRESS_IMAGE")
	if image == "" {
		m, err := deploy.LoadManifest()
		if err != nil {
			t.Fatal(err)
		}
		image = m.Images["songstress"]
	}
	port := os.Getenv("SONGSTRESS_E2E_PORT")
	if port == "" {
		port = "18090"
	}
	base := "http://127.0.0.1:" + port

	clock := time.Now()
	lap := func(what string) {
		t.Logf("⏱ %-38s %6.1fs", what, time.Since(clock).Seconds())
		clock = time.Now()
	}

	pull(t, image)
	lap("pull the songstress image")
	music := fixtureLibrary(t, image)
	lap("generate the 3-track fixture library")

	dir := filepath.Join(t.TempDir(), "songstress")
	install := []string{"install", "--yes", "--music-dir", music, "--install-dir", dir, "--port", port, "--telemetry=false"}
	ndPort := os.Getenv("SONGSTRESS_E2E_NAVIDROME_PORT")
	if ndPort != "" {
		install = append(install, "--navidrome-port", ndPort)
	} else {
		ndPort = "4533"
	}
	if os.Getenv("SONGSTRESS_IMAGE") != "" {
		install = append(install, "--songstress-image", image)
	}
	t.Cleanup(func() { teardown(t, dir) })
	run(t, cli, install...)
	lap("songstress install (FRESH, verified)")

	env := readEnv(t, dir)
	pb := &client{t: t, base: base}
	pb.signIn(env["SONGSTRESS_ADMIN_EMAIL"], env["SONGSTRESS_ADMIN_PASSWORD"])
	pb.expectAppSignIn(env["SONGSTRESS_ADMIN_EMAIL"], env["SONGSTRESS_ADMIN_PASSWORD"])
	pb.expectHealthy()
	pb.expectHeartbeat()
	pb.expectNavidromeVerified()
	expectNavidromeAnswers(t, ndPort)
	lap("health + admin sign-in + heartbeat + Navidrome")

	songs := pb.waitForSongs(3)
	lap("Navidrome scanned the fixture library")
	pb.expectRangeStream(songs[0])
	lap("Range request streams 206")
	pb.expectSetupComplete()

	// A re-run must change nothing a running stack depends on — no file at all.
	before := generatedFiles(t, dir)
	out := run(t, cli, "install", "--yes", "--install-dir", dir)
	lap("re-run songstress install (idempotent)")
	if after := generatedFiles(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("a re-run with nothing new rewrote the deployment")
	}
	for _, want := range []string{"your compose files and .env are unchanged", "your sign-in is unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("the re-run should say %q:\n%s", want, out)
		}
	}
	again := readEnv(t, dir)
	pb.signIn(again["SONGSTRESS_ADMIN_EMAIL"], again["SONGSTRESS_ADMIN_PASSWORD"])
	pb.expectNavidromeVerified()
	pb.expectRangeStream(songs[0])
}

// ── setup ───────────────────────────────────────────────────────────────────

func cliBinary(t *testing.T) string {
	if bin := os.Getenv("SONGSTRESS_CLI"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "songstress")
	cmd := exec.Command("go", "build", "-o", bin, "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the CLI: %v\n%s", err, out)
	}
	return bin
}

func pull(t *testing.T, image string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", image).Run() == nil {
		return // already present (a local override, or pulled before)
	}
	run(t, "docker", "pull", "--quiet", image)
}

// fixtureLibrary writes three tagged FLAC tones with the songstress image's
// own ffmpeg (as the current user, so the files are ours to clean up).
func fixtureLibrary(t *testing.T, image string) string {
	music := filepath.Join(t.TempDir(), "music")
	album := filepath.Join(music, "Fixture Artist", "Fixture Album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, tone := range []struct {
		title string
		hz    int
	}{{"Tone A", 440}, {"Tone B", 554}, {"Tone C", 659}} {
		name := fmt.Sprintf("%02d - %s.flac", i+1, tone.title)
		run(t, "docker", "run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"-v", music+":/out", "--entrypoint", "ffmpeg", image,
			"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=20", tone.hz),
			"-c:a", "flac",
			"-metadata", "title="+tone.title, "-metadata", "artist=Fixture Artist",
			"-metadata", "album=Fixture Album", "-metadata", fmt.Sprintf("track=%d", i+1),
			"/out/Fixture Artist/Fixture Album/"+name)
	}
	return music
}

func teardown(t *testing.T, dir string) {
	if t.Failed() {
		out, _ := exec.Command("docker", "compose", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"), "logs", "--no-color", "--tail", "200").CombinedOutput()
		t.Logf("stack logs:\n%s", out)
	}
	out, err := exec.Command("docker", "compose", "--project-directory", dir, "-f", filepath.Join(dir, "compose.yaml"), "down", "-v", "--remove-orphans").CombinedOutput()
	if err != nil {
		t.Logf("teardown: %v\n%s", err, out)
	}
}

// run runs a command that must succeed and returns its output; the CLI's
// output (what an operator reads) is logged with -v.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	if testing.Verbose() && strings.HasSuffix(name, "songstress") {
		t.Logf("%s", out)
	}
	return string(out)
}

// generatedFiles is every file the installer wrote, but the lock (which
// records when it ran).
func generatedFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "songstress.lock.json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

// expectNavidromeAnswers: the bundled Navidrome's published port reaches it,
// as another Subsonic app would.
func expectNavidromeAnswers(t *testing.T, port string) {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + port + "/ping")
	if err != nil {
		t.Fatalf("Navidrome's port %s: %v", port, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Navidrome's port %s: HTTP %d", port, resp.StatusCode)
	}
}

func readEnv(t *testing.T, dir string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return deploy.ParseEnv(b)
}

// ── assertions against the running server ───────────────────────────────────

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) get(path string, header map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.base+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (c *client) json(path string, out any) {
	c.t.Helper()
	resp, body := c.get(path, map[string]string{"Authorization": c.token})
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, out) != nil {
		c.t.Fatalf("GET %s: HTTP %d %s", path, resp.StatusCode, body)
	}
}

func (c *client) signIn(email, password string) {
	c.t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"identity":%q,"password":%q}`, email, password))
	resp, err := http.Post(c.base+"/api/collections/_superusers/auth-with-password", "application/json", body)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var auth struct{ Token string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&auth) != nil || auth.Token == "" {
		c.t.Fatalf("superuser sign-in: HTTP %d", resp.StatusCode)
	}
	c.token = auth.Token
}

// expectAppSignIn: the seeded pair signs in to the app (web, desktop, mobile)
// as an admin — the account the operator actually uses.
func (c *client) expectAppSignIn(email, password string) {
	c.t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"identity":%q,"password":%q}`, email, password))
	resp, err := http.Post(c.base+"/api/collections/users/auth-with-password", "application/json", body)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var auth struct {
		Token  string
		Record struct {
			Role string `json:"role"`
		}
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&auth) != nil || auth.Token == "" {
		c.t.Fatalf("app sign-in as %s: HTTP %d", email, resp.StatusCode)
	}
	if auth.Record.Role != "admin" {
		c.t.Fatalf("%s signed in as %q, want admin", email, auth.Record.Role)
	}
}

func (c *client) expectHealthy() {
	c.t.Helper()
	if resp, _ := c.get("/api/health", nil); resp.StatusCode != http.StatusOK {
		c.t.Fatalf("/api/health: HTTP %d", resp.StatusCode)
	}
}

func (c *client) expectHeartbeat() {
	c.t.Helper()
	var list struct {
		Items []struct {
			LastSeen string `json:"last_seen"`
		} `json:"items"`
	}
	c.json("/api/collections/worker_status/records?sort=created&perPage=1", &list)
	if len(list.Items) == 0 {
		c.t.Fatal("no worker heartbeat")
	}
	seen, err := time.Parse("2006-01-02 15:04:05.999Z07:00", list.Items[0].LastSeen)
	if err != nil || time.Since(seen) > time.Minute {
		c.t.Fatalf("stale worker heartbeat %q (%v)", list.Items[0].LastSeen, err)
	}
}

// expectNavidromeVerified: the connection the server seeded is enabled and its
// last check signed in — /api/songstress/health on v0.32+, the connection's
// own status (set by the installer's test_connection job) on older servers.
func (c *client) expectNavidromeVerified() {
	c.t.Helper()
	// v0.31 answers this path with a 404 or its web app's index.html.
	resp, body := c.get("/api/songstress/health", nil)
	var h struct {
		OK        *bool `json:"ok"`
		Navidrome *bool `json:"navidrome"`
	}
	if resp.StatusCode == http.StatusOK && json.Unmarshal(body, &h) == nil && h.OK != nil {
		if h.Navidrome == nil || !*h.Navidrome {
			c.t.Fatalf("/api/songstress/health: %s", body)
		}
	}
	var conns struct {
		Items []struct {
			Enabled bool   `json:"enabled"`
			Status  string `json:"status"`
		} `json:"items"`
	}
	c.json("/api/collections/connections/records?filter="+url.QueryEscape(`type = "navidrome"`), &conns)
	if len(conns.Items) != 1 || !conns.Items[0].Enabled || conns.Items[0].Status != "ok" {
		c.t.Fatalf("navidrome connection not verified: %+v", conns.Items)
	}
}

func (c *client) waitForSongs(n int) []string {
	c.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var r struct {
			Response struct {
				Random struct {
					Song []struct{ ID string } `json:"song"`
				} `json:"randomSongs"`
			} `json:"subsonic-response"`
		}
		c.json("/nd/rest/getRandomSongs?size=50&f=json", &r)
		if songs := r.Response.Random.Song; len(songs) >= n {
			ids := make([]string, len(songs))
			for i, s := range songs {
				ids[i] = s.ID
			}
			return ids
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("Navidrome lists %d songs, want %d", len(r.Response.Random.Song), n)
		}
		time.Sleep(3 * time.Second)
	}
}

// expectRangeStream: a bounded Range request answers 206 with exactly those
// bytes — served same-origin (v0.32+) or via the redirect to Navidrome (v0.31).
func (c *client) expectRangeStream(songID string) {
	c.t.Helper()
	resp, body := c.get("/nd/rest/stream?id="+url.QueryEscape(songID),
		map[string]string{"Authorization": c.token, "Range": "bytes=0-1023"})
	if resp.StatusCode != http.StatusPartialContent || len(body) != 1024 ||
		!strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-1023/") {
		c.t.Fatalf("stream: HTTP %d, %d bytes, Content-Range %q", resp.StatusCode, len(body), resp.Header.Get("Content-Range"))
	}
	if string(body[:4]) != "fLaC" {
		c.t.Fatalf("stream did not start with a FLAC header: %q", body[:4])
	}
}

func (c *client) expectSetupComplete() {
	c.t.Helper()
	var info struct {
		SetupComplete bool `json:"setupComplete"`
		HasUsers      bool `json:"hasUsers"`
	}
	c.json("/api/songstress/instance", &info)
	if !info.SetupComplete || !info.HasUsers {
		c.t.Fatalf("instance: %+v (want setup complete with the seeded admin)", info)
	}
}
