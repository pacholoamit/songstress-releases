package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is stamped at release time via -ldflags "-X ...cmd.Version=vX.Y.Z".
var Version = "dev"

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Run:   func(*cobra.Command, []string) { fmt.Println("songstress", Version) },
	})
}
