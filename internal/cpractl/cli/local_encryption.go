package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func newLocalEncryptionCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "encryption",
		Short: "Create or inspect a protected local encryption key",
		Long: "Create or inspect a local wrapping key using explicit absolute paths. " +
			"These operations do not contact CPRa, open its state, or activate configuration. " +
			"The installation scope does not select key ownership; declare the service reader explicitly.",
	}
	for _, action := range []string{"init", "inspect"} {
		var key secureconfig.KeyFileOptions
		var groupID int
		command := &cobra.Command{Use: action, Args: cobra.NoArgs}
		if action == "init" {
			command.Short = "Create an absent protected 32-byte key; existing keys and permissions are never replaced"
		} else {
			command.Short = "Validate an existing key and report only its non-secret identity"
		}
		command.Flags().StringVar(&key.Path, "key-file", "", "required absolute key path outside the state directory")
		command.Flags().StringVar(&key.DataDirectory, "data-dir", "", "required absolute state-directory boundary; it is not opened or created")
		command.Flags().IntVar(&groupID, "reader-group-id", 0, "Unix service reader group; provisioning requires root and creates a root-owned key")
		command.Flags().StringVar(&key.ReaderSID, "reader-sid", "", "Windows service reader SID; provisioning requires an elevated administrator")
		command.MarkFlagsMutuallyExclusive("reader-group-id", "reader-sid")
		command.RunE = func(cmd *cobra.Command, _ []string) error {
			if key.Path == "" || key.DataDirectory == "" {
				return errors.New("--key-file and --data-dir require explicit absolute paths")
			}
			if cmd.Flags().Changed("reader-group-id") {
				key.ReaderGroupID = &groupID
			}
			if cmd.Flags().Changed("reader-sid") && key.ReaderSID == "" {
				return errors.New("--reader-sid requires an explicit Windows service SID")
			}
			var id string
			if action == "init" {
				var err error
				id, err = secureconfig.GenerateLocalKeyFile(cmd.Context(), key)
				if err != nil {
					return fmt.Errorf("initialize encryption key: %w", err)
				}
			} else {
				wrapper, err := secureconfig.LoadLocalKeyFile(cmd.Context(), key)
				if err != nil {
					return fmt.Errorf("inspect encryption key: %w", err)
				}
				id = wrapper.ID()
			}
			err := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				KeyID string `json:"key_id"`
			}{KeyID: id})
			if err != nil && action == "init" {
				return fmt.Errorf("key created but identity output failed; use cpractl local encryption inspect with the same paths and reader options: %w", err)
			}
			return err
		}
		root.AddCommand(command)
	}
	return root
}
