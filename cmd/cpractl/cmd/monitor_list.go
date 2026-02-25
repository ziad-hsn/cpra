package cmd

import (
	"context"
	"fmt"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/cmd/cpractl/output"
	"cpra/cmd/cpractl/client"
)

var (
	flagEnabled   bool
	flagPulseType string
	flagPageSize  int32
)

func init() {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List monitors",
		RunE:  runMonitorList,
	}

	cmd.Flags().BoolVar(&flagEnabled, "enabled", false, "Filter by enabled=true")
	cmd.Flags().StringVar(&flagPulseType, "type", "", "Filter by pulse type (http|tcp|icmp)")
	cmd.Flags().Int32Var(&flagPageSize, "page-size", 100, "Page size")

	monitorCmd.AddCommand(cmd)
}

func runMonitorList(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), viper.GetDuration("timeout"))
	defer cancel()

	cli, err := client.New()
	if err != nil {
		return err
	}

	req := &cprav1.ListMonitorsRequest{
		PageSize: flagPageSize,
	}
	if cmd.Flags().Changed("enabled") {
		req.Enabled = &flagEnabled
	}
	if flagPulseType != "" {
		req.PulseType = &flagPulseType
	}

	resp, err := cli.MonitorService().ListMonitors(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}

	outFormat := viper.GetString("output")
	f := output.New(outFormat, os.Stdout)

	if outFormat == "table" || outFormat == "wide" {
		rows := make([][]string, 0, len(resp.Msg.Monitors))
		for _, m := range resp.Msg.Monitors {
			rows = append(rows, []string{
				m.Name,
				m.Pulse.Type,
				fmt.Sprintf("%t", m.Enabled),
				m.Pulse.Interval.AsDuration().String(),
			})
		}
		td := output.TableData{
			Headers: []string{"NAME", "TYPE", "ENABLED", "INTERVAL"},
			Rows:    rows,
		}
		return f.Format(td)
	}

	return f.Format(resp.Msg.Monitors)
}
