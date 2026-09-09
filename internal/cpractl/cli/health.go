package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newHealthCommand builds the "health" command, a quick liveness check against
// /api/v1/healthz.
func newHealthCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check server liveness (/api/v1/healthz)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.mustClient()
			if err != nil {
				return err
			}
			if err := c.Health(cmd.Context()); err != nil {
				return fmt.Errorf("server unhealthy: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok %s\n", o.server)
			return nil
		},
	}
}

// newMetricsCommand builds the "metrics" command, which dumps the raw
// Prometheus exposition from /metrics.
func newMetricsCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the Prometheus metrics exposition (/metrics)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := o.mustClient()
			if err != nil {
				return err
			}
			body, err := c.Metrics(cmd.Context())
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), body)
			return err
		},
	}
}
