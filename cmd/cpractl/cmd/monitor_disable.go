package cmd

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/cmd/cpractl/client"
)

func init() {
	disableCmd := &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a monitor by ID",
		Args:  cobra.ExactArgs(1),
		RunE:  runMonitorDisable,
	}
	monitorCmd.AddCommand(disableCmd)
}

func runMonitorDisable(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx, cancel := context.WithTimeout(cmd.Context(), viper.GetDuration("timeout"))
	defer cancel()

	cli, err := client.New()
	if err != nil {
		return err
	}

	_, err = cli.MonitorService().DisableMonitor(ctx, connect.NewRequest(&cprav1.DisableMonitorRequest{Id: id}))
	if err != nil {
		return err
	}

	fmt.Printf("monitor %q disabled\n", id)
	return nil
}
