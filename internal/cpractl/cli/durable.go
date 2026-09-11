package cli

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

func addDurableCommands(get *cobra.Command, o *options) {
	var cursor string
	var limit int
	history := &cobra.Command{Use: "history monitor_id", Short: "Show retained incident and action events", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		v, err := c.History(cmd.Context(), args[0], cursor, limit)
		if err != nil {
			return err
		}
		return newPrinter(cmd, o).render(v, func(w io.Writer) error {
			rows := make([][]string, 0, len(v.Events))
			for _, e := range v.Events {
				rows = append(rows, []string{e.ID, e.At.Format(time.RFC3339), e.Type, e.Color, displayEndpoint(e.ActionID, e.Endpoint), e.Outcome})
			}
			if err := table(w, []string{"EVENT", "TIME", "TYPE", "COLOR", "ENDPOINT", "OUTCOME"}, rows); err != nil {
				return err
			}
			if v.NextCursor != "" {
				_, err := fmt.Fprintf(w, "Next cursor: %s\n", v.NextCursor)
				return err
			}
			return nil
		})
	}}
	history.Flags().StringVar(&cursor, "cursor", "", "Continue a history page")
	history.Flags().IntVar(&limit, "limit", 100, "Event limit (1..500)")
	state := &cobra.Command{Use: "state [monitor_id]", Short: "Show persistence health and action outcomes", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		v, err := c.State(cmd.Context(), id)
		if err != nil {
			return err
		}
		return newPrinter(cmd, o).render(v, func(w io.Writer) error {
			if err := kv(w, [][2]string{{"Mode", v.Storage.Mode}, {"Ready", fmtBool(v.Storage.Ready)}, {"Committed index", strconv.FormatUint(v.Storage.CommittedIndex, 10)}, {"Monitor ID", v.MonitorID}}); err != nil {
				return err
			}
			rows := make([][]string, 0, len(v.Actions))
			for _, a := range v.Actions {
				rows = append(rows, []string{a.ID, a.Kind, a.Color, displayEndpoint(a.ID, a.Endpoint), string(a.State), a.Outcome})
			}
			return table(w, []string{"ACTION", "KIND", "COLOR", "ENDPOINT", "STATE", "OUTCOME"}, rows)
		})
	}}
	sloCmd := &cobra.Command{Use: "slo", Short: "Show the current five-minute latency targets and attainment", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, err := o.mustClient()
		if err != nil {
			return err
		}
		v, err := c.SLO(cmd.Context())
		if err != nil {
			return err
		}
		return newPrinter(cmd, o).render(v, func(w io.Writer) error {
			if _, err := fmt.Fprintf(w, "Window: %ds; queue target: %.1fms; result target: %.1fms; complete coverage: %t\n", v.WindowSeconds, v.QueueTargetMS, v.ResultTargetMS, v.CoverageComplete); err != nil {
				return err
			}
			rows := make([][]string, 0, len(v.Reports))
			for _, r := range v.Reports {
				rows = append(rows, []string{r.Driver, strconv.FormatUint(r.Samples, 10), measurement(r.Queue.P99), measurement(r.Result.P99), measurementPercent(r.QueueAttainment), measurementPercent(r.ResultAttainment), strconv.FormatUint(r.Missed, 10), strconv.FormatUint(r.Timeouts, 10), strconv.FormatUint(r.Overdue, 10), r.Condition})
			}
			return table(w, []string{"DRIVER", "SAMPLES", "QUEUE P99 MS", "RESULT P99 MS", "QUEUE ATTAINMENT", "RESULT ATTAINMENT", "MISSED", "TIMEOUTS", "OVERDUE", "CONDITION"}, rows)
		})
	}}
	get.AddCommand(history, state, sloCmd)
}
func measurement(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f", *value)
}

func displayEndpoint(actionID string, endpoint int) string {
	if actionID == "" {
		return "-"
	}
	return strconv.Itoa(endpoint + 1)
}
func measurementPercent(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f%%", *value*100)
}
