package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func addManagementOperationReads(get *cobra.Command, o *options) {
	list := cpra.ListOptions{Limit: 100}
	var results, validation bool
	command := &cobra.Command{Use: "operations [ID]", Aliases: []string{"operation"}, Short: "Read one operation or one bounded page of operation progress", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if (results || validation) && len(args) != 1 {
			return errors.New("--results and --validation require one operation ID")
		}
		if list.Limit < 1 || list.Limit > 500 {
			return errors.New("operation page limit must be between 1 and 500")
		}
		if len(args) == 1 {
			if !safeManagementHandle(args[0]) {
				return errors.New("operation ID must be the exact server-issued handle or a canonical UUID")
			}
			if cmd.Flags().Changed("monitor-id") {
				return errors.New("monitor-id applies only to operation list reads")
			}
			if !results && !validation {
				for _, flag := range []string{"limit", "cursor"} {
					if cmd.Flags().Changed(flag) {
						return errors.New("limit and cursor require an operation list read, --results or --validation")
					}
				}
			}
		}
		if cmd.Flags().Changed("monitor-id") {
			if _, _, err := managementAddressParts([]string{"monitor", list.MonitorID}, true); err != nil {
				return errors.New("--monitor-id must select one exact stable monitor ID")
			}
		}
		client, err := o.mustClient()
		if err != nil {
			return err
		}
		defer client.CloseIdleConnections()
		var reply managementReply
		if len(args) == 1 {
			if validation {
				reply, err = managementResponse(client.Operations.Validation(cmd.Context(), args[0], cpra.ValidationPageOptions{Limit: list.Limit, Cursor: list.Cursor}))
			} else if results {
				reply, err = managementResponse(client.Operations.ExecutionResult(cmd.Context(), args[0], cpra.ExecutionResultPageOptions{Limit: list.Limit, Cursor: list.Cursor}))
			} else {
				reply, err = managementResponse(client.Operations.Get(cmd.Context(), args[0]))
			}
		} else {
			reply, err = managementResponse(client.Operations.List(cmd.Context(), list))
		}
		if err != nil {
			return err
		}
		if validation && o.output != formatJSON && o.output != formatYAML {
			defer clear(reply.data)
			return writeManagementValidation(cmd, reply.data)
		}
		if results && o.output != formatJSON && o.output != formatYAML {
			defer clear(reply.data)
			var result api.Operation
			if json.Unmarshal(reply.data, &result) != nil {
				return errors.New("invalid execution result page")
			}
			return writeManagementExecutionResults(cmd, o, result)
		}
		return writeManagementReply(cmd, o, reply, false, false)
	}}
	command.Flags().BoolVar(&results, "results", false, "read one execution-result page for the selected operation")
	command.Flags().BoolVar(&validation, "validation", false, "read one retained validation page with safe field diagnostics")
	command.MarkFlagsMutuallyExclusive("results", "validation")
	command.Flags().IntVar(&list.Limit, "limit", 100, "page size (1..500); no automatic page collection")
	command.Flags().StringVar(&list.Cursor, "cursor", "", "continue the original operation list or execution-result page with the same limit")
	command.Flags().StringVar(&list.MonitorID, "monitor-id", "", "exact original Monitor target ID; shared-resource operations are not included")
	get.AddCommand(command)
}

func operationValidated(value *bool) string {
	if value == nil {
		return "unavailable"
	}
	if *value {
		return "yes"
	}
	return "no"
}

func writeManagementOperation(cmd *cobra.Command, value api.Operation) error {
	return kv(cmd.OutOrStdout(), [][2]string{{"Operation", managementDisplay(value.ID)}, {"State", managementDisplay(value.State)}, {"Uploaded", operationCount(value.Uploaded)}, {"Committed", operationCount(value.Committed)}, {"Applied", operationCount(value.Applied)}, {"Validated", operationValidated(value.Validated)}})
}

func writeManagementOperations(cmd *cobra.Command, data []byte) error {
	var page api.OperationList
	if json.Unmarshal(data, &page) != nil {
		return errors.New("invalid operation page")
	}
	rows := make([][]string, 0, len(page.Items))
	for _, operation := range page.Items {
		rows = append(rows, []string{managementDisplay(operation.ID), managementDisplay(operation.State), operationCount(operation.Uploaded), operationCount(operation.Committed), operationCount(operation.Applied), operationValidated(operation.Validated)})
	}
	if err := table(cmd.OutOrStdout(), []string{"OPERATION", "STATE", "UPLOADED", "COMMITTED", "APPLIED", "VALIDATED"}, rows); err != nil {
		return err
	}
	if page.NextCursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(page.NextCursor))
		return err
	}
	return nil
}

func writeManagementValidation(cmd *cobra.Command, data []byte) error {
	var page api.ValidationResultPage
	if json.Unmarshal(data, &page) != nil || api.ValidateValidationResultPage(page) != nil {
		return errors.New("invalid validation result page")
	}
	if err := kv(cmd.OutOrStdout(), [][2]string{{"Operation", managementDisplay(page.OperationID)}, {"Valid", fmt.Sprint(page.Summary.Valid)}}); err != nil {
		return err
	}
	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		cause := api.ValidationMessage(item.Reason)
		if cause == "" && item.Issue != "" {
			cause = "validation failed; inspect the resource and its references"
		}
		field := api.SafeValidationField(item.Field)
		if field != "" {
			cause = field + ": " + cause
		}
		rows = append(rows, []string{fmt.Sprint(item.Ordinal), managementDisplay(item.Kind + "/" + item.ID), managementDisplay(item.Source), fmt.Sprintf("%d/%d", item.SourceDocument, item.SourceItem), managementDisplay(item.Change), cause})
	}
	if err := table(cmd.OutOrStdout(), []string{"ORDINAL", "RESOURCE", "SOURCE", "DOCUMENT/ITEM", "CHANGE", "DIAGNOSTIC"}, rows); err != nil {
		return err
	}
	if page.NextCursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(page.NextCursor))
		return err
	}
	return nil
}
