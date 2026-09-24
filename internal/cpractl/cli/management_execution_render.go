package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func writeManagementExecutionResults(cmd *cobra.Command, o *options, value api.Operation) error {
	if err := writeManagementOperation(cmd, value); err != nil {
		return err
	}
	availability := "unavailable"
	if value.ExecutionResult != nil {
		availability = managementDisplay(value.ExecutionResult.State)
	}
	details := [][2]string{{"Execution results", availability}}
	var counts *api.ExecutionResultCounts
	if result := value.ExecutionResult; result != nil {
		counts = result.Counts
		if summary := result.Summary; summary != nil {
			details = append(details, [2]string{"Result ID", managementDisplay(summary.ResultID)},
				[2]string{"Finalized at", managementObservedTime(summary.FinalizedAt)}, [2]string{"Expires at", managementObservedTime(summary.ExpiresAt)})
			if counts == nil {
				counts = &api.ExecutionResultCounts{Processed: summary.Processed, Accepted: summary.Accepted, Unchanged: summary.Unchanged,
					Conflicts: summary.Conflicts, DependencyBlocked: summary.DependencyBlocked, Unattempted: summary.Unattempted,
					ChildPending: summary.ChildPending, ChildApplied: summary.ChildApplied, ChildFailed: summary.ChildFailed,
					ChildSuperseded: summary.ChildSuperseded, ChildInvalidated: summary.ChildInvalidated}
			}
		}
	}
	if counts == nil {
		details = append(details, [2]string{"Execution counts", "unavailable"})
	} else {
		for _, count := range []struct {
			name  string
			value int64
		}{{"Processed", counts.Processed}, {"Accepted", counts.Accepted}, {"Unchanged", counts.Unchanged}, {"Conflicts", counts.Conflicts},
			{"Dependency blocked", counts.DependencyBlocked}, {"Unattempted", counts.Unattempted}, {"Pending children", counts.ChildPending},
			{"Applied children", counts.ChildApplied}, {"Failed children", counts.ChildFailed}, {"Superseded children", counts.ChildSuperseded}, {"Invalidated children", counts.ChildInvalidated}} {
			details = append(details, [2]string{count.name, strconv.FormatInt(count.value, 10)})
		}
	}
	if err := kv(cmd.OutOrStdout(), details); err != nil {
		return err
	}
	if len(value.Items) != 0 {
		headers := []string{"INPUT", "RESOURCE", "CONFIGURATION DECISION", "COMMITTED", "CONTROLLER STATE", "CONTROLLER OUTCOME", "APPLIED", "RESTORE INVALIDATED"}
		if o.output == formatWide {
			headers = append(headers, "PLAN", "CHILD OPERATION", "SOURCE", "RESTORE ID")
		}
		rows := make([][]string, 0, len(value.Items))
		for _, item := range value.Items {
			state, outcome, operation := "unavailable", "unavailable", "unavailable"
			invalidated, restoreID := "unavailable", "unavailable"
			if child := item.ChildDisposition; child != nil {
				state, operation = managementDisplay(child.State), managementDisplay(child.OperationID)
				if child.Outcome != "" {
					outcome = managementDisplay(child.Outcome)
				}
				if child.InvalidatedByRestore != "" {
					invalidated, restoreID = "yes", managementDisplay(child.InvalidatedByRestore)
				} else if child.State == "completed" || child.State == "failed" || child.State == "partial" {
					invalidated = "no"
				}
			}
			row := []string{operationCount(item.InputOrdinal), managementDisplay(item.ID), managementDisplay(item.CatalogDecision),
				operationValidated(item.Committed), state, outcome, operationValidated(item.Applied), invalidated}
			if o.output == formatWide {
				row = append(row, operationCount(item.PlanOrdinal), operation, managementDisplay(item.Source), restoreID)
			}
			rows = append(rows, row)
		}
		if err := table(cmd.OutOrStdout(), headers, rows); err != nil {
			return err
		}
	}
	if value.NextCursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(value.NextCursor))
		return err
	}
	return nil
}
