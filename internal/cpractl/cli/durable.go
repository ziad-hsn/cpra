package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func addDurableCommands(get *cobra.Command, o *options) {
	opts := cpra.ListOptions{Limit: 100}
	history := &cobra.Command{Use: "history stable-monitor-id", Short: "Read one retained monitor event page", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := managementAddressParts([]string{"monitor", args[0]}, true); err != nil {
			return err
		}
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		opts.MonitorID = args[0]
		reply, err := managementResponse(c.History(cmd.Context(), opts))
		if err != nil {
			return err
		}
		return writeManagementReply(cmd, o, reply, false, false)
	}}
	history.Flags().StringVar(&opts.Cursor, "cursor", "", "continue the original monitor timeline page")
	history.Flags().IntVar(&opts.Limit, "limit", 100, "page size (1..500); no implicit timeline collection")
	state := &cobra.Command{Use: "state", Short: "Show controller and storage observations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		r, err := c.State(cmd.Context())
		if err != nil {
			return managementFailure(err)
		}
		return writeState(newPrinter(cmd, o), r.Data)
	}}
	slo := &cobra.Command{Use: "slo", Short: "Show reported latency targets, attainment and pause exposure", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		defer c.CloseIdleConnections()
		r, err := c.SLO.Get(cmd.Context())
		if err != nil {
			return managementFailure(err)
		}
		return writeSLO(newPrinter(cmd, o), r.Data)
	}}
	get.AddCommand(history, state, slo)
}
func writeState(p *printer, s api.State) error {
	return p.render(s, func(w io.Writer) error {
		return kv(w, [][2]string{{"Live", fmtBool(s.Live)}, {"Ready", fmtBool(s.Ready)}, {"Readiness reason", managementDisplay(s.ReadinessReason)}, {"Controller ready", observedBool(s.ControllerAvailable, s.ControllerReady)}, {"Controller reason", observedText(s.ControllerAvailable, s.ControllerReason)}, {"Storage mode", observedText(s.Storage.Available, s.Storage.Mode)}, {"Applied index", observedCount(s.Storage.Available, s.Storage.AppliedIndex)}, {"Storage bytes", observedCount(s.Storage.Available && s.Storage.BytesAvailable, s.Storage.Bytes)}, {"Unknown actions", observedCount(s.UnknownActionsAvailable, s.UnknownActions)}, {"Projection fresh", observedBool(s.ProjectionAvailable, s.ProjectionFresh)}, {"Projection age ms", observedNumber(s.ProjectionAvailable, s.ProjectionAgeMS)}})
	})
}
func writeSLO(p *printer, s api.SLOView) error {
	return p.render(s, func(w io.Writer) error {
		if err := kv(w, [][2]string{{"Available", fmtBool(s.Available)}, {"Window", observedText(s.Available, s.Window)}, {"Queue target ms", observedNumber(s.Available, s.QueueTargetMS)}, {"Result target ms", observedNumber(s.Available, s.ResultTargetMS)}, {"Coverage gap", observedBool(s.Available, s.CoverageGap)}}); err != nil {
			return err
		}
		rows := make([][]string, 0, len(s.Reports))
		for _, r := range s.Reports {
			queueAttainment, resultAttainment := "unavailable", "unavailable"
			if s.Available {
				queueAttainment = measurementPercent(r.QueueAttainment)
				resultAttainment = measurementPercent(r.ResultAttainment)
			}
			rows = append(rows, []string{managementDisplay(r.Driver), observedText(s.Available, r.Pipeline), observedCount(s.Available, r.Samples), percentile(s.Available, r.QueueDelay), percentile(s.Available, r.TotalLatency), queueAttainment, resultAttainment, observedCount(s.Available, r.Missed), observedCount(s.Available, r.Timeouts), observedCount(s.Available, r.Overdue), observedCount(s.Available, r.PausedMonitors), observedNumber(s.Available, r.PausedMonitorSeconds), managementDisplay(r.Condition)})
		}
		if err := table(w, []string{"DRIVER", "PIPELINE", "SAMPLES", "QUEUE P99 MS", "RESULT P99 MS", "QUEUE ATTAINMENT", "RESULT ATTAINMENT", "MISSED", "TIMEOUTS", "OVERDUE", "PAUSED", "PAUSE MONITOR-SECONDS", "CONDITION"}, rows); err != nil {
			return err
		}
		if !s.Available {
			_, err := fmt.Fprintln(w, "Observation unavailable")
			return err
		}
		return nil
	})
}
