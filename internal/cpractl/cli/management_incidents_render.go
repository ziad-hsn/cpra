package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func managementObservedTime(value time.Time) string {
	if value.IsZero() {
		return "unavailable"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func writeManagementIncidents(cmd *cobra.Command, o *options, data []byte, describe bool) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid incident response")
	}
	var incidents []api.Incident
	if items, ok := fields["items"]; ok {
		if json.Unmarshal(items, &incidents) != nil {
			return errors.New("invalid incident page")
		}
	} else {
		var incident api.Incident
		if json.Unmarshal(data, &incident) != nil {
			return errors.New("invalid incident response")
		}
		incidents = []api.Incident{incident}
	}
	if describe && len(incidents) == 1 {
		i := incidents[0]
		actor := i.AcknowledgedBy
		if actor == "" {
			actor = "unacknowledged"
		}
		return kv(cmd.OutOrStdout(), [][2]string{{"Incident", managementDisplay(i.ID)}, {"Monitor", managementDisplay(i.MonitorID)}, {"State", managementDisplay(i.State)}, {"Revision", managementDisplay(i.Revision)}, {"Acknowledged by", managementDisplay(actor)}, {"Acknowledged at", managementObservedTime(i.AcknowledgedAt)}, {"Dismissed", strconv.FormatBool(i.Dismissed)}, {"Opened at", managementObservedTime(i.OpenedAt)}, {"Closed at", managementObservedTime(i.ClosedAt)}})
	}
	headers := []string{"INCIDENT", "MONITOR", "STATE", "ACKNOWLEDGED BY", "DISMISSED", "REVISION"}
	if o.output == formatWide {
		headers = append(headers, "OPENED AT", "CLOSED AT")
	}
	rows := make([][]string, 0, len(incidents))
	for _, i := range incidents {
		actor := i.AcknowledgedBy
		if actor == "" {
			actor = "unacknowledged"
		}
		row := []string{managementDisplay(i.ID), managementDisplay(i.MonitorID), managementDisplay(i.State), managementDisplay(actor), strconv.FormatBool(i.Dismissed), managementDisplay(i.Revision)}
		if o.output == formatWide {
			row = append(row, managementObservedTime(i.OpenedAt), managementObservedTime(i.ClosedAt))
		}
		rows = append(rows, row)
	}
	if err := table(cmd.OutOrStdout(), headers, rows); err != nil {
		return err
	}
	var cursor string
	_ = json.Unmarshal(fields["nextCursor"], &cursor)
	if cursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(cursor))
		return err
	}
	return nil
}
