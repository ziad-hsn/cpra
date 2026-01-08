package cmd

import (
	"context"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/cmd/cpractl/output"
	"cpra/cmd/cpractl/client"
)

func init() {
	getCmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a monitor by ID or slug",
		Args:  cobra.ExactArgs(1),
		RunE:  runMonitorGet,
	}
	monitorCmd.AddCommand(getCmd)
}

func runMonitorGet(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx, cancel := context.WithTimeout(cmd.Context(), viper.GetDuration("timeout"))
	defer cancel()

	cli, err := client.New()
	if err != nil {
		return err
	}

	resp, err := cli.MonitorService().GetMonitor(ctx, connect.NewRequest(&cprav1.GetMonitorRequest{Id: id}))
	if err != nil {
		return err
	}

	f := output.New(viper.GetString("output"), os.Stdout)
	return f.Format(resp.Msg)
}
