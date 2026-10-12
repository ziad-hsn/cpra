package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func newResumeCommand(o *options) *cobra.Command {
	var filenames []string
	var settings collection.Options
	var attempt string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{Use: "resume operation/ID -f FILE_OR_URL [-f FILE_OR_URL ...]", Short: "Verify and continue the original collection through activation",
		Long: "Verify the original source bytes and ordered resource identities, complete missing uploads, then continue whole-input validation and conditional activation on the original operation. Use the same source order and unchanged files as apply. An already complete upload still requires input verification. --attempt continues a known server-side verification attempt; its staged bytes remain authoritative. Stopping the client does not cancel server work. No replacement operation or automatic mutation retry is performed.",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimPrefix(args[0], "operation/")
			if id == args[0] || !safeManagementHandle(id) || attempt != "" && !safeUploadAttemptID(attempt) || timeout < 0 || timeout != 0 && !wait {
				return errors.New("use resume operation/ID -f INPUT; --timeout requires --wait and must not be negative")
			}
			sources, cleanup, err := collectionInputSources(cmd, filenames, attempt == "")
			if err != nil {
				return err
			}
			defer cleanup()
			client, err := o.mustClient()
			if err != nil {
				return managementFailure(err)
			}
			defer client.CloseIdleConnections()
			result, resumeErr := collection.ResumeFiles(cmd.Context(), client.Operations, id, sources, collection.ReselectionOptions{AttemptID: attempt, Sources: settings})
			_, outputErr := fmt.Fprintf(cmd.ErrOrStderr(), "Operation: %s\n", id)
			if safeUploadAttemptID(result.VerificationAttemptID) {
				_, attemptErr := fmt.Fprintf(cmd.ErrOrStderr(), "Verification attempt: %s\n", result.VerificationAttemptID)
				outputErr = errors.Join(outputErr, attemptErr)
			}
			if resumeErr == nil && wait {
				ctx, cancel := collectionWaitContext(cmd.Context(), timeout)
				defer cancel()
				result, resumeErr = collection.Wait(ctx, client.Operations, result)
			}
			if result.Operation.ID != "" {
				outputErr = errors.Join(outputErr, writeCollectionOperation(cmd, o, result.Operation))
			}
			if resumeErr != nil || outputErr != nil {
				return errors.Join(managementFailure(resumeErr), outputErr)
			}
			if wait {
				return collectionCompletionError(result.Operation)
			}
			return nil
		}}
	addCollectionInputFlags(cmd, &filenames, &settings)
	cmd.Flags().StringVar(&attempt, "attempt", "", "explicit original verification attempt UUID; reconcile it before submitting more work")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the original operation's retained execution result")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "maximum wait after activation admission (0 waits until completion or cancellation)")
	return cmd
}
