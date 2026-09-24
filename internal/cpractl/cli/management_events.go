package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func addManagementEventReads(get *cobra.Command, o *options) {
	list := cpra.ListOptions{Limit: 100}
	command := &cobra.Command{Use: "events --monitor-id ID", Short: "Read one bounded v2 monitor audit page, including actors, notes and evidence references", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := managementAddressParts([]string{"monitor", list.MonitorID}, true); err != nil {
			return errors.New("--monitor-id must select one exact stable monitor ID")
		}
		if list.Limit < 1 || list.Limit > 500 {
			return errors.New("event page limit must be between 1 and 500")
		}
		client, err := o.mustClient()
		if err != nil {
			return err
		}
		defer client.CloseIdleConnections()
		reply, err := managementResponse(client.History(cmd.Context(), list))
		if err != nil {
			return err
		}
		return writeManagementReply(cmd, o, reply, false, false)
	}}
	command.Flags().StringVar(&list.MonitorID, "monitor-id", "", "required exact stable monitor ID")
	command.Flags().IntVar(&list.Limit, "limit", 100, "page size (1..500); no implicit timeline collection")
	command.Flags().StringVar(&list.Cursor, "cursor", "", "continue the original monitor timeline page")
	get.AddCommand(command)
}

func writeManagementEvents(cmd *cobra.Command, o *options, data []byte) error {
	var page api.EventList
	if json.Unmarshal(data, &page) != nil {
		return errors.New("invalid event page")
	}
	headers := []string{"EVENT", "TIME", "KIND", "ACTION", "ACTOR", "OUTCOME", "REASON", "NOTE", "EVIDENCE REFERENCES"}
	if o.output == formatWide {
		headers = append(headers, "MONITOR", "INCIDENT", "EXECUTION REVISION", "CONTROL REVISION")
	}
	rows := make([][]string, 0, len(page.Items))
	for _, event := range page.Items {
		refs, _ := json.Marshal(event.EvidenceRefs)
		row := []string{managementDisplay(event.ID), managementObservedTime(event.Time), managementDisplay(event.Kind), managementDisplay(event.ActionID), managementDisplay(event.Actor), managementDisplay(event.Outcome), managementDisplay(event.Reason), managementDisplay(event.Note), string(refs)}
		if o.output == formatWide {
			row = append(row, managementDisplay(event.MonitorID), managementDisplay(event.IncidentID), managementDisplay(event.ExecutionRevision), managementDisplay(event.ControlRevision))
		}
		rows = append(rows, row)
	}
	if err := table(cmd.OutOrStdout(), headers, rows); err != nil {
		return err
	}
	if page.NextCursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(page.NextCursor))
		return err
	}
	return nil
}
