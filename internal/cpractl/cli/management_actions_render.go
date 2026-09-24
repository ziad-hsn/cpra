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

func optionalManagementTime(value *time.Time) string {
	if value == nil {
		return "unavailable"
	}
	return managementObservedTime(*value)
}

func writeManagementActions(cmd *cobra.Command, o *options, data []byte, describe bool) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid action response")
	}
	var actions []api.Action
	if items, ok := fields["items"]; ok {
		if json.Unmarshal(items, &actions) != nil {
			return errors.New("invalid action page")
		}
	} else {
		var action api.Action
		if json.Unmarshal(data, &action) != nil {
			return errors.New("invalid action response")
		}
		actions = []api.Action{action}
	}
	if describe && len(actions) == 1 {
		a := actions[0]
		rows := [][2]string{{"Action", managementDisplay(a.ID)}, {"Monitor", managementDisplay(a.MonitorID)}, {"Monitor UID", managementDisplay(a.IncarnationUID)}, {"Incident", managementDisplay(a.IncidentID)}, {"Kind", managementDisplay(a.Kind)}, {"Provider state", managementDisplay(a.State)}, {"Provider outcome", managementDisplay(a.Outcome)}, {"Held", strconv.FormatBool(a.Held)}, {"Executor fenced", strconv.FormatBool(a.ExecutorFenced)}, {"Review revision", managementDisplay(a.ReviewRevision)}, {"Execution ID", managementDisplay(a.ExecutionID)}, {"Execution revision", managementDisplay(a.ExecutionRevision)}, {"Created at", optionalManagementTime(a.CreatedAt)}, {"Updated at", optionalManagementTime(a.UpdatedAt)}}
		if a.Review != nil {
			references, _ := json.Marshal(a.Review.EvidenceRefs)
			rows = append(rows, [2]string{"Operator assertion", managementDisplay(string(a.Review.Resolution))}, [2]string{"Reviewed by", managementDisplay(a.Review.Actor)}, [2]string{"Reviewed at", managementObservedTime(a.Review.ReviewedAt)}, [2]string{"Review reason", managementDisplay(a.Review.Reason)}, [2]string{"Review note", managementDisplay(a.Review.Note)}, [2]string{"Evidence references", string(references)}, [2]string{"Review conflict", strconv.FormatBool(a.Review.Conflict)})
		} else {
			rows = append(rows, [2]string{"Operator assertion", "not reviewed"})
		}
		for _, evidence := range []struct {
			name string
			data *api.ActionEvidence
		}{{"Late provider evidence", a.LateEvidence}, {"Conflicting provider evidence", a.ConflictingEvidence}} {
			if evidence.data != nil {
				encoded, _ := json.Marshal(evidence.data)
				rows = append(rows, [2]string{evidence.name, string(encoded)})
			}
		}
		return kv(cmd.OutOrStdout(), rows)
	}
	headers := []string{"ACTION", "MONITOR", "KIND", "PROVIDER STATE", "HELD", "OPERATOR ASSERTION", "REVIEW REVISION"}
	if o.output == formatWide {
		headers = append(headers, "PROVIDER OUTCOME", "EXECUTOR FENCED", "CREATED AT")
	}
	rows := make([][]string, 0, len(actions))
	for _, a := range actions {
		assertion := "not reviewed"
		if a.Review != nil {
			assertion = string(a.Review.Resolution)
		}
		row := []string{managementDisplay(a.ID), managementDisplay(a.MonitorID), managementDisplay(a.Kind), managementDisplay(a.State), strconv.FormatBool(a.Held), managementDisplay(assertion), managementDisplay(a.ReviewRevision)}
		if o.output == formatWide {
			row = append(row, managementDisplay(a.Outcome), strconv.FormatBool(a.ExecutorFenced), optionalManagementTime(a.CreatedAt))
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
