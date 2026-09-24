package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func newApplyCommand(o *options) *cobra.Command {
	var filenames []string
	var settings collection.Options
	var wait bool
	var dryRun string
	var fileProfile string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "apply -f FILE_OR_URL [-f FILE_OR_URL ...]", Short: "Validate and activate a complete collection", Args: cobra.NoArgs,
		Long: "Freeze all selected inputs, upload inactive staging, validate the complete collection, then activate its original operation. Returns after activation admission unless --wait is set. Stopping the client does not cancel server work. Use get operation ID --results to read bounded retained results. Opt into --file-profile cpra.file.base.v1 to permit later upload recovery from the exact original source bytes and source order; plain apply keeps the ordinary SDK collection contract.",
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			if dryRun != "" && dryRun != "server" || timeout < 0 || timeout != 0 && !wait || dryRun != "" && wait {
				return errors.New("use --dry-run=server or --wait; --timeout requires --wait and must not be negative")
			}
			settings.MaxResources = 10000
			frozen, err := freezeCollectionInputsProfile(cmd, filenames, settings, fileProfile)
			if err != nil {
				return err
			}
			defer func() {
				if closeErr := frozen.Close(); closeErr != nil {
					err = &managementCommandError{"could not remove private collection staging", errors.Join(err, closeErr)}
				}
			}()
			var operations *cpra.OperationsService
			if frozen.Len() > 0 {
				client, clientErr := o.mustClient()
				if clientErr != nil {
					return managementFailure(clientErr)
				}
				defer client.CloseIdleConnections()
				operations = client.Operations
			}
			if dryRun == "server" {
				preflight, preflightErr := collection.Diff(cmd.Context(), operations, frozen)
				if preflightErr != nil && !errors.Is(preflightErr, collection.ErrPreflightRejected) {
					return managementFailure(preflightErr)
				}
				report, reportErr := collectionDiffReport(cmd.Context(), frozen, preflight)
				if reportErr != nil {
					return reportErr
				}
				if err := writeCollectionDiff(cmd, o, report); err != nil {
					return err
				}
				return managementFailure(preflightErr)
			}
			result, applyErr := collection.Apply(cmd.Context(), operations, frozen)
			if safeManagementHandle(result.OperationID) {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Operation: %s\n", result.OperationID); err != nil {
					return errors.Join(applyErr, err)
				}
			}
			if applyErr == nil && wait && !result.Noop {
				ctx, cancel := collectionWaitContext(cmd.Context(), timeout)
				defer cancel()
				result, applyErr = collection.Wait(ctx, operations, result)
			}
			if result.Noop {
				result.Operation = api.Operation{State: "noop", ItemCount: api.Pointer(int64(0))}
			}
			if result.Operation.ID != "" || result.Noop {
				if renderErr := writeCollectionOperation(cmd, o, result.Operation); renderErr != nil {
					return errors.Join(managementFailure(applyErr), renderErr)
				}
			}
			if applyErr != nil {
				return managementFailure(applyErr)
			}
			if wait {
				return collectionCompletionError(result.Operation)
			}
			return nil
		}}
	addCollectionInputFlags(cmd, &filenames, &settings)
	cmd.Flags().StringVar(&fileProfile, "file-profile", "", "explicit file normalization profile: cpra.file.base.v1 (default preserves ordinary apply)")
	cmd.Flags().Lookup("max-source-bytes").Usage = "cumulative source byte quota (0 uses staging quota for ordinary apply, 64 MiB with --file-profile)"
	cmd.Flags().Lookup("max-staging-bytes").Usage = "private temporary staging byte quota (0 uses 1 GiB for ordinary apply, 512 MiB with --file-profile)"
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the original operation's retained execution result")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "maximum execution wait after admission (0 waits until completion or cancellation)")
	cmd.Flags().StringVar(&dryRun, "dry-run", "", "server: validate and show changes without allocating an operation")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &managementCommandError{"invalid apply flags; consult cpractl apply --help", err}
	})
	return cmd
}

func newWaitCommand(o *options) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{Use: "wait operation/ID", Short: "Wait for the original operation without submitting mutations", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		id := ""
		if len(args) == 1 {
			id = strings.TrimPrefix(args[0], "operation/")
			if id == args[0] {
				return errors.New("use wait operation/ID or wait operation ID")
			}
		} else if args[0] == "operation" || args[0] == "operations" {
			id = args[1]
		}
		if !safeManagementHandle(id) || timeout < 0 {
			return errors.New("wait requires an exact operation handle and a nonnegative timeout")
		}
		client, err := o.mustClient()
		if err != nil {
			return managementFailure(err)
		}
		defer client.CloseIdleConnections()
		ctx, cancel := collectionWaitContext(cmd.Context(), timeout)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return managementFailure(err)
		}
		first, err := client.Operations.Get(ctx, id)
		if err != nil {
			return managementFailure(err)
		}
		if first.Data.ID != id || first.OperationID != "" && first.OperationID != id {
			return errors.New("operation progress identity mismatch")
		}
		observed := first.Data
		if observed.IdentityFormat == commitment.Format {
			result, waitErr := collection.Wait(ctx, client.Operations, collection.Result{OperationID: id, Operation: observed})
			observed, err = result.Operation, waitErr
		} else if observed.IdentityFormat != "" || observed.ExecutionResult != nil {
			err = cpra.ErrExecutionResultUnsupported
		} else {
			last, waitErr := client.Operations.Wait(ctx, id)
			if last != nil && last.Data.ID == id {
				observed = last.Data
			}
			err = waitErr
		}
		if renderErr := writeCollectionOperation(cmd, o, observed); renderErr != nil {
			return errors.Join(managementFailure(err), renderErr)
		}
		if err != nil {
			return managementFailure(err)
		}
		return collectionCompletionError(observed)
	}}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "maximum wait duration (0 waits until completion or cancellation)")
	return cmd
}

func collectionWaitContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

func writeCollectionOperation(cmd *cobra.Command, o *options, operation api.Operation) error {
	if o.output != formatJSON && o.output != formatYAML {
		if operation.ExecutionResult != nil {
			return writeManagementExecutionResults(cmd, o, operation)
		}
		return writeManagementOperation(cmd, operation)
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	return writeManagementReply(cmd, o, managementReply{data: raw}, false, false)
}

func collectionCompletionError(operation api.Operation) error {
	if result := operation.ExecutionResult; result != nil && result.Summary != nil {
		if result.State == "ready" && result.Summary.Outcome == "completed" && result.Summary.Accepted+result.Summary.Unchanged == result.Summary.ItemCount && result.Summary.Accepted == result.Summary.ChildApplied {
			return nil
		}
		return errors.New("collection execution did not complete successfully; inspect the reported result and counts")
	}
	switch operation.State {
	case "completed", "succeeded", "noop":
		return nil
	default:
		return errors.New("operation did not complete successfully; inspect the reported state and counts")
	}
}
