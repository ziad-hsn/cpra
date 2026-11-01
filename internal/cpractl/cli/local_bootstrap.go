package cli

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func newLocalBootstrapCommand() *cobra.Command {
	root := &cobra.Command{Use: "bootstrap", Short: "Inspect or conditionally discard an unadmitted encrypted loading stage while stopped"}
	for _, discard := range []bool{false, true} {
		request := localadmin.BootstrapRequest{}
		use, short := "inspect", "Authenticate the selected stage without activating input or executing providers"
		if discard {
			use, short = "discard", "Audit and discard only the exact unadmitted loading stage"
		}
		command := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if request.DataDirectory == "" || request.RuntimeConfig == "" {
				return errors.New("--data-dir and --runtime-config are both required; use cpractl local bootstrap inspect --data-dir /absolute/state --runtime-config /absolute/runtime.yaml while the owner is stopped")
			}
			if discard {
				report, err := localadmin.DiscardBootstrap(cmd.Context(), request)
				if report.AttemptID != "" {
					err = errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(report))
				}
				return err
			}
			report, err := localadmin.InspectBootstrap(cmd.Context(), request)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}}
		command.Flags().StringVar(&request.DataDirectory, "data-dir", "", "required absolute stopped state directory")
		command.Flags().StringVar(&request.RuntimeConfig, "runtime-config", "", "runtime settings containing the original encryption configuration")
		command.Flags().StringVar(&request.Stage, "stage", "published", "published or temporary (interrupted first-input loading)")
		if discard {
			command.Flags().StringVar(&request.Identity, "identity", "", "exact identity returned by local bootstrap inspect")
			command.Flags().StringVar(&request.Actor, "actor", "", "administrator identity recorded in the protected audit")
			command.Flags().StringVar(&request.Reason, "reason", "", "required reason; do not include credentials")
			command.Flags().StringVar(&request.AuditDirectory, "audit-dir", "", "protected audit directory outside state (default: DATA-DIR-bootstrap-audit)")
		}
		root.AddCommand(command)
	}
	return root
}
