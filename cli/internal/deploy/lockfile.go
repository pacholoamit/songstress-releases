package deploy

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const lockName = "songstress.lock.json"

// Lock is the installer state written next to the compose files. It carries
// NO secrets (those live only in .env, mode 0600) — it exists so re-runs can
// default to previous answers and `update` can compare pins.
type Lock struct {
	CLIVersion string            `json:"cli_version"`
	CreatedAt  string            `json:"created_at"`
	Platform   string            `json:"platform"`
	Answers    Answers           `json:"answers"`
	Pins       map[string]string `json:"pins"`
}

func WriteLock(dir string, l Lock) error {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, lockName), append(b, '\n'), 0o644)
}

// ReadLock returns ok=false when no lock exists (fresh install).
func ReadLock(dir string) (Lock, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if errors.Is(err, fs.ErrNotExist) {
		return Lock{}, false, nil
	}
	if err != nil {
		return Lock{}, false, err
	}
	var l Lock
	if err := json.Unmarshal(b, &l); err != nil {
		return Lock{}, false, err
	}
	return l, true, nil
}
