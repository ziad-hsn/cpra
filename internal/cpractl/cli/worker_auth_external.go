//go:build externaljobs

package cli

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func registerLocalExtensions(root *cobra.Command) {
	root.AddCommand(newLocalWorkerAuthCommand())
}

func newLocalWorkerAuthCommand() *cobra.Command {
	root := &cobra.Command{Use: "worker-auth", Short: "Provision workers in an exclusively locked, stopped local store"}
	for _, action := range []string{"issue", "rotate", "set-grants", "revoke", "list", "reprovision"} {
		request := localadmin.WorkerAuthenticationRequest{Action: action}
		var expiry string
		command := &cobra.Command{Use: action + " WORKER", Short: workerAuthDescription(action), Args: cobra.ExactArgs(1)}
		if action == "list" {
			command.Use, command.Args = "list", cobra.NoArgs
		}
		command.Flags().StringVar(&request.DataDirectory, "data-dir", "", "required absolute path to the existing stopped state directory")
		command.Flags().StringVar(&request.Actor, "actor", "", "required current named management operator for local audit and policy fencing")
		if action == "issue" || action == "reprovision" || action == "set-grants" {
			command.Flags().StringVar(&request.GrantsFile, "grants-file", "", "required protected absolute JSON file outside state, at most 1 MiB; explicit empty grants deny all scopes")
		}
		if action == "issue" || action == "rotate" || action == "reprovision" {
			command.Flags().StringVar(&request.TokenOutput, "token-output", "", "required absent absolute file outside state; receives the worker bearer once with protected permissions")
			command.Flags().StringVar(&expiry, "expires-at", "", "RFC3339 expiry or never; omission preserves an existing worker expiry")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if action != "list" {
				request.WorkerID = args[0]
			}
			if cmd.Flags().Changed("expires-at") {
				at := time.Time{}
				if expiry != "never" {
					var err error
					at, err = time.Parse(time.RFC3339, expiry)
					if err != nil {
						return errors.New("--expires-at must be RFC3339 or never")
					}
				}
				request.ExpiresAt = &at
			}
			report, err := localadmin.AdministerWorkerAuthentication(cmd.Context(), request)
			if report.Outcome != "" || report.IntendedRevision != "" {
				if outputErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report); outputErr != nil {
					return errors.Join(err, outputErr)
				}
			}
			return err
		}
		root.AddCommand(command)
	}
	return root
}

func workerAuthDescription(action string) string {
	switch action {
	case "issue":
		return "Issue a fresh worker token and explicit grants for an absent identity"
	case "rotate":
		return "Rotate one token without overlap, preserving worker identity and grants"
	case "set-grants":
		return "Replace one worker's explicit grants without issuing a token"
	case "revoke":
		return "Permanently revoke one worker identity without deleting its record"
	case "list":
		return "Read worker identity, revisions, grants and restore state without tokens or verifiers"
	default:
		return "Provision a new or reset worker in restored state with fresh credentials and explicit grants"
	}
}
