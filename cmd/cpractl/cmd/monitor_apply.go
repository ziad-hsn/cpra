package cmd

import (
	"context"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"cpra/cmd/cpractl/client"
	"cpra/cmd/cpractl/output"
	cprav1 "cpra/gen/cpra/v1"
)

var applyFile string
var applyConcurrency int

func init() {
	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Create or update a monitor from a YAML file",
		RunE:  runMonitorApply,
	}
	applyCmd.Flags().StringVarP(&applyFile, "file", "f", "", "Path to monitor YAML file (required)")
	applyCmd.Flags().IntVar(&applyConcurrency, "concurrency", 4, "Number of concurrent apply requests")
	applyCmd.MarkFlagRequired("file")
	monitorCmd.AddCommand(applyCmd)
}

func runMonitorApply(cmd *cobra.Command, args []string) error {
	monitors, err := loadMonitorsFromFile(applyFile)
	if err != nil {
		return err
	}

	cli, err := client.New()
	if err != nil {
		return err
	}

	timeout := viper.GetDuration("timeout")
	if applyConcurrency < 1 {
		applyConcurrency = 1
	}

	results := make([]*cprav1.Monitor, len(monitors))
	sem := make(chan struct{}, applyConcurrency)
	g, ctx := errgroup.WithContext(cmd.Context())
	for i, monitor := range monitors {
		i, monitor := i, monitor
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			defer func() { <-sem }()

			updateCtx, updateCancel := context.WithTimeout(ctx, timeout)
			resp, err := cli.MonitorService().UpdateMonitor(updateCtx, connect.NewRequest(&cprav1.UpdateMonitorRequest{Monitor: monitor}))
			updateCancel()
			if err != nil {
				if connect.CodeOf(err) != connect.CodeNotFound {
					return err
				}
				createCtx, createCancel := context.WithTimeout(ctx, timeout)
				createResp, createErr := cli.MonitorService().CreateMonitor(createCtx, connect.NewRequest(&cprav1.CreateMonitorRequest{Monitor: monitor}))
				createCancel()
				if createErr != nil {
					return createErr
				}
				results[i] = createResp.Msg.Monitor
				return nil
			}
			results[i] = resp.Msg
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	f := output.New(viper.GetString("output"), os.Stdout)
	return formatMonitorsOutput(results, viper.GetString("output"), f)
}
