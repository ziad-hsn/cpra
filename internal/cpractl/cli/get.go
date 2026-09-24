package cli

import (
	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// newGetCommand uses stable resource identities and bounded v2 observations.
func newGetCommand(o *options) *cobra.Command {
	get := &cobra.Command{Use: "get", Short: "Display configuration resources and runtime observations", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		return runManagementGet(cmd, o, args, false, cpra.ListOptions{Limit: 100})
	}}
	for _, kind := range []string{"queues", "pools", "systems"} {
		opts := cpra.ListOptions{Limit: 100}
		command := &cobra.Command{Use: kind, Short: "Read one bounded page of " + kind, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.mustClient()
			if err != nil {
				return err
			}
			defer c.CloseIdleConnections()
			switch kind {
			case "queues":
				r, err := c.Queues.List(cmd.Context(), opts)
				if err != nil {
					return managementFailure(err)
				}
				return writeQueues(newPrinter(cmd, o), r.Data)
			case "pools":
				r, err := c.Pools.List(cmd.Context(), opts)
				if err != nil {
					return managementFailure(err)
				}
				return writePools(newPrinter(cmd, o), r.Data)
			default:
				r, err := c.Systems.List(cmd.Context(), opts)
				if err != nil {
					return managementFailure(err)
				}
				return writeSystems(newPrinter(cmd, o), r.Data)
			}
		}}
		command.Flags().IntVar(&opts.Limit, "limit", 100, "page size (1..500); no automatic page collection")
		command.Flags().StringVar(&opts.Cursor, "cursor", "", "continue the original observation page")
		get.AddCommand(command)
	}
	get.AddCommand(&cobra.Command{Use: "config", Short: "Show reported process settings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		r, err := c.RuntimeConfig(cmd.Context())
		if err != nil {
			return managementFailure(err)
		}
		return writeConfig(newPrinter(cmd, o), r.Data)
	}})
	addDurableCommands(get, o)
	addManagementGetCommands(get, o)
	return get
}
