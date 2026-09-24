package cli

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newHealthCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "health", Short: "Check server liveness (/api/v2/healthz)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		r, err := c.Live(cmd.Context())
		if err != nil {
			return fmt.Errorf("server unhealthy: %w", managementFailure(err))
		}
		if !r.Data.Available {
			return fmt.Errorf("server unhealthy: liveness was not confirmed")
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok %s\n", o.server)
		return err
	}}
}
func newReadyCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "ready", Short: "Check admission readiness (/api/v2/readyz)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		r, err := c.Ready(cmd.Context())
		if err != nil {
			return fmt.Errorf("server not ready: %w", managementFailure(err))
		}
		if !r.Data.Available {
			return fmt.Errorf("server not ready: readiness was not confirmed")
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ready %s\n", o.server)
		return err
	}}
}
func newMetricsCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "metrics", Short: "Stream the Prometheus metrics exposition (/metrics)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		return managementFailure(c.Prometheus(cmd.Context(), cmd.OutOrStdout()))
	}}
}
