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

// ImageTag is the tag of an image reference ("" when it has none). A digest
// (@sha256:…) is not a tag; a ":port" in the registry host is not either.
func ImageTag(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ""
	}
	return ref[i+1:]
}

// ReleaseVersion parses a release tag ("v0.31.0" / "0.31") into major, minor,
// patch; ok=false for anything else (edge, main-<sha>, a local build).
func ReleaseVersion(image string) (v [3]int, ok bool) {
	tag := strings.TrimPrefix(ImageTag(image), "v")
	parts := strings.SplitN(tag, ".", 3)
	if len(parts) < 2 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// OlderRelease reports whether release a is older than release b (both must
// parse as releases).
func OlderRelease(a, b string) bool {
	va, okA := ReleaseVersion(a)
	vb, okB := ReleaseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}
