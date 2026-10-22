// Package deploy renders a Songstress deployment: pinned compose files, the
// .env with generated secrets, and the songstress.lock.json install state.
package deploy

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
)

// manifest.json is the tested pin set this CLI release ships with. It is the
// only source of image references — templates never hardcode tags. Every pin
// must exist in its registry (TestPinnedImagesResolve), or every install fails.
//
//go:embed manifest.json
var manifestJSON []byte

type Manifest struct {
	Images map[string]string `json:"images"`
}

func LoadManifest() (Manifest, error) {
	var m Manifest
	err := json.Unmarshal(manifestJSON, &m)
	return m, err
}

// imageTag is the tag of an image reference ("" when it has none). A digest
// (@sha256:…) is not a tag; a ":port" in the registry host is not either.
func imageTag(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ""
	}
	return ref[i+1:]
}

// AppVersion is the APP_VERSION a legacy server needs from compose (in-app
// version banner, telemetry, the worker heartbeat): the songstress image's tag,
// without the "v" of a release tag ("v0.31.0" → "0.31.0"). It follows the pin,
// so the two can't drift apart the way a separately maintained number did
// (0.21.0 shipped beside a v0.21.0 image that was never published). Images from
// v0.32 stamp their own version at build, and the compose files leave it alone.
func AppVersion(image string) string {
	tag := imageTag(image)
	if tag == "" {
		return "dev"
	}
	if len(tag) > 1 && tag[0] == 'v' && tag[1] >= '0' && tag[1] <= '9' {
		return tag[1:]
	}
	return tag
}

// LegacyServer reports a songstress release older than v0.32. Those stream by
// REDIRECTING players to Navidrome — to the managed Navidrome's published port
// (SONGSTRESS_NAVIDROME_PORT) or to NAVIDROME_PUBLIC_URL — and carry no
// version stamp, so compose must set APP_VERSION. From v0.32 every stream and
// image is served by Songstress itself and the image names its own version, so
// none of those settings are written. Non-release tags (a local build, edge,
// main-<sha>) are treated as current.
func LegacyServer(image string) bool {
	tag := strings.TrimPrefix(imageTag(image), "v")
	parts := strings.SplitN(tag, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major == 0 && minor < 32
}
