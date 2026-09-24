package cli

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func newResumeUploadCommand(o *options) *cobra.Command {
	var filenames []string
	var settings collection.Options
	var attemptID string
	cmd := &cobra.Command{Use: "resume-upload operation/ID [-f FILE_OR_URL ...]", Short: "Recover only the original collection's missing upload",
		Long: "Read the original operation, verify the exact original source bytes, then upload only its missing resources. This command never validates, activates or cancels configuration. Use the same -f order as the original profiled apply; directories expand in lexical order and recursion requires -R. Comments, whitespace, empty sources and source boundaries must match. URL inputs use a separate unauthenticated client. --attempt reads an existing attempt before reading source bytes; sources are optional after its source upload or when the original upload is already complete. Attempts disappear after server restart. A lost response may leave the attempt handle unknown; requests are not automatically retried.",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimPrefix(args[0], "operation/")
			if id == args[0] || !safeManagementHandle(id) || attemptID != "" && !safeUploadAttemptID(attemptID) {
				return errors.New("use resume-upload operation/ID with an exact original operation handle and optional canonical attempt UUID")
			}
			sources, cleanup, err := collectionInputSources(cmd, filenames, false)
			if err != nil {
				return err
			}
			defer cleanup()
			client, err := o.mustClient()
			if err != nil {
				return managementFailure(err)
			}
			defer client.CloseIdleConnections()
			result, recoverErr := collection.Reselect(cmd.Context(), client.Operations, id, sources, collection.ReselectionOptions{AttemptID: attemptID, Sources: settings})
			report := uploadRecoveryReportFor(id, attemptID, result)
			if err := writeUploadRecovery(cmd, o, report); err != nil {
				return errors.Join(uploadRecoveryFailure(recoverErr, report.AttemptID == ""), err)
			}
			return uploadRecoveryFailure(recoverErr, report.AttemptID == "")
		}}
	addCollectionInputFlags(cmd, &filenames, &settings)
	cmd.Flags().StringVar(&attemptID, "attempt", "", "existing server-issued attempt UUID; read it before continuing the original upload")
	cmd.Flags().Lookup("max-source-bytes").Usage = "cumulative raw source byte quota (0 uses 64 MiB; maximum 64 MiB)"
	cmd.Flags().Lookup("max-staging-bytes").Usage = "private raw-source staging byte quota (0 uses 64 MiB; actual raw input never exceeds 64 MiB)"
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &managementCommandError{"invalid upload recovery flags; consult cpractl resume-upload --help", err}
	})
	return cmd
}

func newGetUploadAttemptCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{Use: "upload-attempt OPERATION_ID ATTEMPT_ID", Short: "Read one upload recovery attempt without renewing or changing it", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, attemptID := strings.TrimPrefix(args[0], "operation/"), args[1]
			if !safeManagementHandle(id) || !safeUploadAttemptID(attemptID) {
				return errors.New("upload-attempt requires an exact original operation handle and canonical attempt UUID")
			}
			client, err := o.mustClient()
			if err != nil {
				return managementFailure(err)
			}
			defer client.CloseIdleConnections()
			report := uploadRecoveryReport{OperationID: id, AttemptID: attemptID}
			response, readErr := client.Operations.GetReselection(cmd.Context(), id, attemptID)
			if readErr == nil && response != nil {
				report.Attempt = safeUploadAttempt(id, attemptID, &response.Data)
				if report.Attempt == nil {
					readErr = errors.New("invalid upload attempt observation")
				} else {
					report.Complete = report.Attempt.Phase == "completed"
				}
			}
			if err := writeUploadRecovery(cmd, o, report); err != nil {
				return errors.Join(managementFailure(readErr), err)
			}
			return managementFailure(readErr)
		}}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &managementCommandError{"invalid upload-attempt flags; consult cpractl get upload-attempt --help", err}
	})
	return cmd
}

// Reports intentionally exclude full operation rows, private commitments and
// diagnostics. Even an error can retain the last confirmed IDs and counters.
type uploadRecoveryReport struct {
	OperationID string                            `json:"operationID"`
	AttemptID   string                            `json:"attemptID,omitempty"`
	Complete    bool                              `json:"complete"`
	Operation   *uploadRecoveryOperation          `json:"operation,omitempty"`
	Attempt     *api.CollectionReselectionAttempt `json:"attempt,omitempty"`
}

type uploadRecoveryOperation struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	ItemCount *int64 `json:"itemCount,omitempty"`
	Uploaded  *int64 `json:"uploaded,omitempty"`
	Committed *int64 `json:"committed,omitempty"`
	Applied   *int64 `json:"applied,omitempty"`
}

func uploadRecoveryReportFor(id, requestedAttempt string, result collection.ReselectionResult) uploadRecoveryReport {
	report := uploadRecoveryReport{OperationID: id, AttemptID: requestedAttempt}
	if result.OperationID != id {
		return report
	}
	if result.Operation.ID == id {
		original := result.Operation
		state := "unrecognized"
		switch original.State {
		case "pending", "staging", "uploading", "validating", "validated", "rejected", "interrupted", "applying", "committed", "completed", "partial", "failed", "canceled", "cancelled", "expired", "invalidated":
			state = original.State
		}
		report.Operation = &uploadRecoveryOperation{ID: id, State: state, ItemCount: safeUploadCount(original.ItemCount), Uploaded: safeUploadCount(original.Uploaded), Committed: safeUploadCount(original.Committed), Applied: safeUploadCount(original.Applied)}
		report.Complete = result.Complete && report.Operation.ItemCount != nil && report.Operation.Uploaded != nil && *report.Operation.ItemCount > 0 && *report.Operation.Uploaded == *report.Operation.ItemCount
	}
	report.Attempt = safeUploadAttempt(id, requestedAttempt, result.Attempt)
	if report.Attempt != nil {
		report.AttemptID = report.Attempt.ID
	}
	return report
}

func safeUploadCount(value *int64) *int64 {
	if value == nil || *value < 0 || *value > 10_000 {
		return nil
	}
	return api.Pointer(*value)
}

func safeUploadAttemptID(id string) bool { return len(id) == 36 && safeManagementHandle(id) }

func safeUploadAttempt(id, requested string, value *api.CollectionReselectionAttempt) *api.CollectionReselectionAttempt {
	if value == nil || value.OperationID != id || !safeUploadAttemptID(value.ID) || requested != "" && value.ID != requested ||
		value.NormalizationProfile != collection.FileNormalizationProfile || !value.Phase.Valid() || value.ExpiresAt.IsZero() ||
		value.SourceCount < 1 || value.SourceCount > 1000 || value.SourcesCompleted < 0 || value.SourcesCompleted > value.SourceCount ||
		value.RawBytes < 0 || value.RawBytes > 64<<20 || value.NextOffset < 0 || value.NextOffset > value.RawBytes ||
		value.NextSource < 0 || value.NextSource > value.SourceCount || value.OperationUploaded < 0 || value.OperationUploaded > 10_000 ||
		value.ErrorCode != nil && !value.ErrorCode.Valid() {
		return nil
	}
	copy := *value
	if value.ErrorCode != nil {
		copy.ErrorCode = api.Pointer(*value.ErrorCode)
	}
	return &copy
}

func uploadRecoveryFailure(err error, unknownAttempt bool) error {
	if err == nil {
		return nil
	}
	if unknownAttempt && errors.Is(err, cpra.ErrAmbiguous) {
		return &managementCommandError{"upload recovery outcome is unconfirmed and no attempt handle was returned; inspect the original operation before continuing; no request was retried", err}
	}
	return managementFailure(err)
}

func writeUploadRecovery(cmd *cobra.Command, o *options, report uploadRecoveryReport) error {
	if o.output == formatJSON || o.output == formatYAML {
		raw, err := json.Marshal(report)
		if err != nil {
			return errors.New("could not encode upload recovery observation")
		}
		return writeManagementReply(cmd, o, managementReply{data: raw}, false, false)
	}
	rows := [][2]string{{"Operation", report.OperationID}, {"Attempt", strOrDash(report.AttemptID)}, {"Upload complete", strconv.FormatBool(report.Complete)}}
	if operation := report.Operation; operation != nil {
		rows = append(rows, [2]string{"State", operation.State}, [2]string{"Items", operationCount(operation.ItemCount)}, [2]string{"Uploaded", operationCount(operation.Uploaded)}, [2]string{"Committed", operationCount(operation.Committed)}, [2]string{"Applied", operationCount(operation.Applied)})
	}
	if attempt := report.Attempt; attempt != nil {
		rows = append(rows, [2]string{"Attempt phase", string(attempt.Phase)}, [2]string{"Sources", strconv.FormatInt(attempt.SourceCount, 10)}, [2]string{"Sources complete", strconv.FormatInt(attempt.SourcesCompleted, 10)}, [2]string{"Raw bytes", strconv.FormatInt(attempt.RawBytes, 10)}, [2]string{"Next source", strconv.FormatInt(attempt.NextSource, 10)}, [2]string{"Next offset", strconv.FormatInt(attempt.NextOffset, 10)}, [2]string{"Attempt observed upload", strconv.FormatInt(attempt.OperationUploaded, 10)}, [2]string{"Attempt expires", attempt.ExpiresAt.UTC().Format(time.RFC3339)})
		if attempt.ErrorCode != nil {
			rows = append(rows, [2]string{"Attempt error", string(*attempt.ErrorCode)})
		}
	}
	return kv(cmd.OutOrStdout(), rows)
}
