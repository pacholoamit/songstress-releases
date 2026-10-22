package cmd

import (
	"bytes"
	"testing"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// `songstress version` and `songstress --version` name the CLI and the
// Songstress release it installs: the CLI's version decides the server's.
func TestVersionNamesTheSongstressRelease(t *testing.T) {
	m, err := deploy.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	want := "songstress " + Version + " (installs Songstress " + deploy.ImageTag(m.Images["songstress"]) + ")\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetArgs(args)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.String() != want {
			t.Errorf("%v printed %q, want %q", args, out.String(), want)
		}
	}
	rootCmd.SetOut(nil)
	rootCmd.SetArgs(nil)
}
