package cli

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func newLocalAuthCommand() *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: "Administer named access in an exclusively locked, stopped local store"}
	for _, action := range []string{"list", "bootstrap", "issue", "rotate", "revoke", "reprovision"} {
		request := localadmin.AuthenticationRequest{Action: action}
		var expiry string
		command := &cobra.Command{Use: action + " PRINCIPAL", Short: localAuthDescription(action), Args: cobra.ExactArgs(1)}
		if action == "list" {
			command.Use, command.Args = "list", cobra.NoArgs
		}
		command.Flags().StringVar(&request.DataDirectory, "data-dir", "", "required absolute path to the stopped state directory")
		if action != "list" && action != "revoke" {
			command.Flags().StringVar(&request.TokenOutput, "token-output", "", "required absent absolute file outside state; receives the bearer once with protected permissions")
			command.Flags().StringVar(&expiry, "expires-at", "", "explicit RFC3339 expiry, or never; omitted rotation preserves expiry, omitted issuance has none")
		}
		if action == "bootstrap" || action == "issue" || action == "reprovision" {
			command.Flags().StringVar(&request.Role, "role", "", "required role: reader or operator")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			if action != "list" {
				request.PrincipalID = args[0]
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
			report, err := localadmin.AdministerAuthentication(cmd.Context(), request)
			// Safe state and intended revision support reconciliation even when a
			// mutation reply is uncertain. No verifier or bearer enters this DTO.
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

func localAuthDescription(action string) string {
	switch action {
	case "list":
		return "Read safe principal, expiry and restore-epoch metadata; token values and verifiers are omitted"
	case "bootstrap":
		return "Initialize access once with an explicitly chosen fresh named principal"
	case "issue":
		return "Issue one fresh token for an absent principal, with no implicit expiry"
	case "rotate":
		return "Replace one principal's verifier without overlap, preserving role and any existing expiry"
	case "revoke":
		return "Revoke one principal in the authoritative durable policy"
	default:
		return "Provision a restored store's current epoch with one new principal, replacing all restored grants"
	}
}
