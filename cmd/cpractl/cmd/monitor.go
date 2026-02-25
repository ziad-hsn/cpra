package cmd

import "github.com/spf13/cobra"

// monitorCmd is the parent for monitor subcommands.
var monitorCmd = &cobra.Command{
    Use:   "monitor",
    Short: "Manage CPRA monitors",
}

func init() {
    rootCmd.AddCommand(monitorCmd)
}
