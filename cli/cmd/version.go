package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pacholoamit/songstress-releases/cli/internal/deploy"
)

// Version is stamped at release time via -ldflags "-X ...cmd.Version=vX.Y.Z".
var Version = "dev"

func init() {
	// `songstress --version` and `songstress version` print the same line.
	rootCmd.Version = versionLine()
	rootCmd.SetVersionTemplate("songstress {{.Version}}\n")
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the CLI version and the Songstress release it installs",
		Run:   func(c *cobra.Command, _ []string) { fmt.Fprintln(c.OutOrStdout(), "songstress", versionLine()) },
	})
}

// versionLine is "v0.2.0 (installs Songstress v0.32.0)": the CLI's version
// decides the server's, since each CLI release pins the images it was tested
// with.
func versionLine() string {
	m, err := deploy.LoadManifest()
	if err != nil {
		return Version
	}
	return Version + " (installs Songstress " + deploy.ImageTag(m.Images["songstress"]) + ")"
}
