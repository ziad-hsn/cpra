package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version info (set at build time via -ldflags)
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

func init() {
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("cpractl %s\n", Version)
			fmt.Printf("  commit: %s\n", GitCommit)
			fmt.Printf("  built:  %s\n", BuildDate)
		},
	}
	rootCmd.AddCommand(versionCmd)
}
