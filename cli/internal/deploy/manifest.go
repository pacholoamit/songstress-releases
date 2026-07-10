// Package deploy renders a Songstress deployment: pinned compose files, the
// .env with generated secrets, and the songstress.lock.json install state.
package deploy

import (
	_ "embed"
	"encoding/json"
)

// manifest.json is the tested pin set this CLI release ships with. It is the
// only source of image references — templates never hardcode tags.
//
//go:embed manifest.json
var manifestJSON []byte

type Manifest struct {
	CLI    string            `json:"cli"`
	Images map[string]string `json:"images"`
}

func LoadManifest() (Manifest, error) {
	var m Manifest
	err := json.Unmarshal(manifestJSON, &m)
	return m, err
}
