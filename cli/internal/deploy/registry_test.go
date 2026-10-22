package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPinnedImagesResolve fails when a pinned image doesn't exist in its
// registry — the CLI once shipped pinned to ghcr.io/…/songstress:v0.21.0, a tag
// that was never published, so every `songstress install` failed. It asks each
// registry anonymously, the way `docker pull` does (HEAD on the manifest, which
// doesn't count against Docker Hub's pull limit). Offline it skips; so do
// `go test -short` and SONGSTRESS_SKIP_REGISTRY=1. CI runs it, which gates every
// manifest bump on the new tag being published.
func TestPinnedImagesResolve(t *testing.T) {
	if testing.Short() || os.Getenv("SONGSTRESS_SKIP_REGISTRY") != "" {
		t.Skip("registry check skipped (-short / SONGSTRESS_SKIP_REGISTRY)")
	}
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for name, ref := range m.Images {
		t.Run(name, func(t *testing.T) {
			ok, err := resolves(context.Background(), client, ref)
			if isOffline(err) {
				t.Skipf("offline, can't reach the registry for %s: %v", ref, err)
			}
			if err != nil {
				t.Fatalf("%s: %v", ref, err)
			}
			if !ok {
				t.Fatalf("%s is not published — every install would fail to pull it", ref)
			}
		})
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][3]string{
		"ghcr.io/pacholoamit/songstress:v0.31.0": {"ghcr.io", "pacholoamit/songstress", "v0.31.0"},
		"deluan/navidrome:0.62.0":                {"docker.io", "deluan/navidrome", "0.62.0"},
		"postgres:15-alpine":                     {"docker.io", "library/postgres", "15-alpine"},
		"localhost:5000/songstress:e2e":          {"localhost:5000", "songstress", "e2e"},
	}
	for ref, want := range cases {
		reg, repo, tag := splitRef(ref)
		if [3]string{reg, repo, tag} != want {
			t.Errorf("splitRef(%q) = %q %q %q, want %v", ref, reg, repo, tag, want)
		}
	}
}

func splitRef(ref string) (registry, repo, tag string) {
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest, tag
	}
	if !found {
		return "docker.io", "library/" + name, tag
	}
	return "docker.io", name, tag
}

func resolves(ctx context.Context, client *http.Client, ref string) (bool, error) {
	registry, repo, tag := splitRef(ref)
	token, err := pullToken(ctx, client, registry, repo)
	if err != nil {
		return false, err
	}
	host := registry
	if registry == "docker.io" {
		host = "registry-1.docker.io"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://"+host+"/v2/"+repo+"/manifests/"+tag, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("registry answered HTTP %d", resp.StatusCode)
}

// pullToken fetches an anonymous pull token for registries that require one.
func pullToken(ctx context.Context, client *http.Client, registry, repo string) (string, error) {
	scope := url.QueryEscape("repository:" + repo + ":pull")
	var endpoint string
	switch registry {
	case "docker.io":
		endpoint = "https://auth.docker.io/token?service=registry.docker.io&scope=" + scope
	case "ghcr.io":
		endpoint = "https://ghcr.io/token?service=ghcr.io&scope=" + scope
	default:
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return "", fmt.Errorf("%s token endpoint answered HTTP %d", registry, resp.StatusCode)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// isOffline: the registry couldn't be reached at all (no DNS, no route,
// timeout) — as opposed to it answering that the tag is missing.
func isOffline(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	var op *net.OpError
	var ue *url.Error
	return errors.As(err, &dns) || errors.As(err, &op) || (errors.As(err, &ue) && ue.Timeout())
}
