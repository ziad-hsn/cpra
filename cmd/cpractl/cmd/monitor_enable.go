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
	enableCmd := &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a monitor by ID",
		Args:  cobra.ExactArgs(1),
		RunE:  runMonitorEnable,
	}
	monitorCmd.AddCommand(enableCmd)
}

func runMonitorEnable(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx, cancel := context.WithTimeout(cmd.Context(), viper.GetDuration("timeout"))
	defer cancel()

	cli, err := client.New()
	if err != nil {
		return err
	}

	_, err = cli.MonitorService().EnableMonitor(ctx, connect.NewRequest(&cprav1.EnableMonitorRequest{Id: id}))
	if err != nil {
		return err
	}

	fmt.Printf("monitor %q enabled\n", id)
	return nil
}
