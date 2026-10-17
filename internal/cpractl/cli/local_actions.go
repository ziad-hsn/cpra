package cli

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func newLocalActionsCommand() *cobra.Command {
	var directory, monitor, cursor string
	var limit int
	c := &cobra.Command{Use: "actions", Short: "Read a bounded page of actions from an exclusively owned stopped store", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			page, err := localadmin.ReadStoppedActions(cmd.Context(), directory, monitor, cursor, limit)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(page)
		}}
	c.Flags().StringVar(&directory, "data-dir", "", "required absolute path to the stopped state directory")
	c.Flags().StringVar(&monitor, "monitor-id", "", "filter by stable monitor ID")
	c.Flags().StringVar(&cursor, "cursor", "", "continue the preceding action page")
	c.Flags().IntVar(&limit, "limit", 100, "maximum actions to return (1–500)")
	return c
}

func newLocalReviewCommand() *cobra.Command {
	var request localadmin.LocalActionReviewRequest
	c := &cobra.Command{Use: "review action/ID", Short: "Record an audited assertion about a stopped action; never replay it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !strings.HasPrefix(args[0], "action/") || len(args[0]) == len("action/") {
				return errors.New("select action/ID from local actions")
			}
			request.ActionID = strings.TrimPrefix(args[0], "action/")
			report, err := localadmin.ReviewStoppedAction(cmd.Context(), request)
			if report.IntendedRevision != "" {
				err = errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(report))
			}
			return err
		}}
	c.Flags().StringVar(&request.DataDirectory, "data-dir", "", "required absolute path to the stopped state directory")
	c.Flags().StringVar(&request.ExpectedRevision, "revision", "", "exact action review revision returned by local actions")
	c.Flags().StringVar(&request.Actor, "actor", "", "administrator identity recorded in history")
	c.Flags().StringVar(&request.Reason, "reason", "", "required reason for this assertion")
	c.Flags().StringVar(&request.Resolution, "resolution", "", "accepted, rejected, or inconclusive; does not change provider facts")
	c.Flags().StringVar(&request.Note, "note", "", "optional audit note")
	c.Flags().StringSliceVar(&request.EvidenceRefs, "evidence", nil, "references to independent evidence (at most eight)")
	return c
}
