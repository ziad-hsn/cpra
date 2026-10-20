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
		rows = append(rows, [2]string{"Failure", diagnosticText(a.Failure)}, [2]string{"Notification cause", diagnosticText(a.Cause)})
		rows = append(rows, [2]string{"Notification color", managementDisplay(a.Color)}, [2]string{"Delivery", managementDisplay(a.DeliveryID)}, [2]string{"Destination", managementDisplay(a.EndpointID)}, [2]string{"Notification episode", managementDisplay(a.NotificationEpisode)}, [2]string{"Queued at", optionalManagementTime(a.QueuedAt)}, [2]string{"Not before", optionalManagementTime(a.NotBefore)}, [2]string{"Deadline", optionalManagementTime(a.Deadline)}, [2]string{"Started at", optionalManagementTime(a.StartedAt)}, [2]string{"Finished at", optionalManagementTime(a.FinishedAt)})
		if n := a.ReviewNotice; n != nil {
			purpose := "Recovery outcome unknown - operator review required"
			if n.Phase == "close" {
				if n.Reason == "health_recovered" {
					purpose = "Health recovered earlier; the recovery action outcome remains unknown"
				} else {
					purpose = "Recovery outcome reviewed"
				}
			}
			rows = append(rows, [2]string{"Review alert", managementDisplay(n.AlertID)}, [2]string{"Recovery action", managementDisplay(n.ActionID)}, [2]string{"Notice phase", managementDisplay(n.Phase)}, [2]string{"Notice purpose", purpose}, [2]string{"Health at notice", managementDisplay(n.Health)})
		}
		if a.Review != nil {
			references, _ := json.Marshal(a.Review.EvidenceRefs)
			rows = append(rows, [2]string{"Operator assertion", managementDisplay(string(a.Review.Resolution))}, [2]string{"Reviewed by", managementDisplay(a.Review.Actor)}, [2]string{"Reviewed at", managementObservedTime(a.Review.ReviewedAt)}, [2]string{"Review reason", managementDisplay(a.Review.Reason)}, [2]string{"Review note", managementDisplay(a.Review.Note)}, [2]string{"Evidence references", string(references)}, [2]string{"Review conflict", strconv.FormatBool(a.Review.Conflict)})
		} else {
			rows = append(rows, [2]string{"Operator assertion", "not reviewed"})
		}
		if f := a.RecoveryFailure; f != nil {
			rows = append(rows, [2]string{"Failed recovery action", managementDisplay(f.ActionID)}, [2]string{"Failed recovery driver", managementDisplay(f.Driver)}, [2]string{"Recovery attempt", fmt.Sprintf("%d of %d", f.Attempt, f.MaxAttempts)}, [2]string{"Notification trigger", managementDisplay(string(f.Trigger))}, [2]string{"Recovery failed at", managementObservedTime(f.FailedAt)})
		}
		if r := a.AgentReceipt; r != nil {
			rows = append(rows, [2]string{"Agent receipt sequence", managementDisplay(r.Sequence)}, [2]string{"Agent observed at", managementObservedTime(r.ObservedAt)})
			for _, field := range []struct {
				name  string
				value *string
			}{{"Agent task", r.TaskID}, {"Agent context", r.ContextID}, {"Agent message", r.MessageID}, {"Agent task state", r.State}} {
				if field.value != nil {
					rows = append(rows, [2]string{field.name, managementDisplay(*field.value)})
				}
			}
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
		headers = append(headers, "PROVIDER OUTCOME", "EXECUTOR FENCED", "CREATED AT", "FAILURE")
	}
	rows := make([][]string, 0, len(actions))
	for _, a := range actions {
		assertion := "not reviewed"
		if a.Review != nil {
			assertion = string(a.Review.Resolution)
		}
		row := []string{managementDisplay(a.ID), managementDisplay(a.MonitorID), managementDisplay(a.Kind), managementDisplay(a.State), strconv.FormatBool(a.Held), managementDisplay(assertion), managementDisplay(a.ReviewRevision)}
		if o.output == formatWide {
			row = append(row, managementDisplay(a.Outcome), strconv.FormatBool(a.ExecutorFenced), optionalManagementTime(a.CreatedAt), diagnosticText(a.Failure))
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
