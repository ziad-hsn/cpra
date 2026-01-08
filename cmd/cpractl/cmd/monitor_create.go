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

var createFile string
var createConcurrency int

func init() {
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new monitor from a YAML file",
		RunE:  runMonitorCreate,
	}
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Path to monitor YAML file (required)")
	createCmd.Flags().IntVar(&createConcurrency, "concurrency", 4, "Number of concurrent create requests")
	createCmd.MarkFlagRequired("file")
	monitorCmd.AddCommand(createCmd)
}

func runMonitorCreate(cmd *cobra.Command, args []string) error {
	monitors, err := loadMonitorsFromFile(createFile)
	if err != nil {
		return err
	}

	cli, err := client.New()
	if err != nil {
		return err
	}

	timeout := viper.GetDuration("timeout")
	if createConcurrency < 1 {
		createConcurrency = 1
	}

	results := make([]*cprav1.Monitor, len(monitors))
	sem := make(chan struct{}, createConcurrency)
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

			reqCtx, reqCancel := context.WithTimeout(ctx, timeout)
			resp, err := cli.MonitorService().CreateMonitor(reqCtx, connect.NewRequest(&cprav1.CreateMonitorRequest{Monitor: monitor}))
			reqCancel()
			if err != nil {
				return err
			}
			results[i] = resp.Msg.Monitor
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	f := output.New(viper.GetString("output"), os.Stdout)
	return formatMonitorsOutput(results, viper.GetString("output"), f)
}
