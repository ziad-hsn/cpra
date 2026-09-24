package cli

import (
	"errors"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

// ErrDifferences indicates a successful diff containing proposed changes.
// It has exit status 1 without an error banner; no mutation was requested.
var ErrDifferences = errors.New("configuration differences found")

type diffExitError struct{ error }

func (e *diffExitError) Unwrap() error { return e.error }

// ExitCode preserves status 1 for ordinary command errors. Diff distinguishes
// changes (1) from validation, input, transport and other failures (2).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var failure *diffExitError
	if errors.As(err, &failure) {
		return 2
	}
	return 1
}

func diffFailure(err error) error {
	if err == nil {
		return nil
	}
	return &diffExitError{err}
}

func newDiffCommand(o *options) *cobra.Command {
	var filenames []string
	var settings collection.Options
	command := &cobra.Command{
		Use:   "diff -f FILE_OR_URL [-f FILE_OR_URL ...]",
		Short: "Validate complete inputs and show proposed configuration changes",
		Long:  "Freeze YAML/JSON resources or legacy manifests from files, directories, stdin (-), or explicitly selected URLs, then request an ephemeral server diff. No resources are created, changed or deleted. Exit status is 0 for no changes, 1 for differences, and 2 for failure.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return diffFailure(&managementCommandError{"diff accepts inputs only through -f; consult cpractl diff --help", err})
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			defer func() {
				if err != nil && err != ErrDifferences {
					err = diffFailure(err)
				}
			}()
			settings.MaxResources = 10000
			frozen, err := freezeCollectionInputs(cmd, filenames, settings)
			if err != nil {
				return err
			}
			defer func() {
				if closeErr := frozen.Close(); closeErr != nil {
					err = &managementCommandError{"could not remove private collection staging", errors.Join(err, closeErr)}
				}
			}()
			var operations collection.Operations
			if frozen.Len() > 0 {
				client, clientErr := o.mustClient()
				if clientErr != nil {
					return managementFailure(clientErr)
				}
				operations = client.Operations
			}
			preflight, err := collection.Diff(cmd.Context(), operations, frozen)
			if err != nil && !errors.Is(err, collection.ErrPreflightRejected) {
				var transport *cpra.TransportError
				if errors.As(err, &transport) && cmd.Context().Err() == nil {
					return &managementCommandError{"preflight response is unavailable; no active write was requested and the request was not retried", err}
				}
				return managementFailure(err)
			}
			report, renderErr := collectionDiffReport(cmd.Context(), frozen, preflight)
			if renderErr != nil {
				return renderErr
			}
			if renderErr = writeCollectionDiff(cmd, o, report); renderErr != nil {
				return renderErr
			}
			if err != nil {
				return &managementCommandError{"collection validation failed; no active changes were made", err}
			}
			if report.Changed {
				return ErrDifferences
			}
			return nil
		},
	}
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return diffFailure(&managementCommandError{"invalid diff flags; consult cpractl diff --help", err})
	})
	addCollectionInputFlags(command, &filenames, &settings)
	return command
}
