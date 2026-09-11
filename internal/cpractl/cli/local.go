package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/platformpath"
)

func newLocalCommand() *cobra.Command {
	var scope string
	root := &cobra.Command{Use: "local", Short: "Manage local configuration, installation, and stopped backups"}
	root.PersistentFlags().StringVar(&scope, "scope", "user", "installation scope: user or system")
	paths := &cobra.Command{Use: "paths", Args: cobra.NoArgs, Short: "Show native configuration and state paths", RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := platformpath.Resolve(scope)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(l)
	}}
	var initAccount string
	init := &cobra.Command{Use: "init", Args: cobra.NoArgs, Short: "Create absent starter files without replacing existing configuration", RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := platformpath.Resolve(scope)
		if err != nil {
			return err
		}
		if err = localadmin.Init(l, initAccount); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Configuration initialized at", l.ConfigDir)
		return nil
	}}
	init.Flags().StringVar(&initAccount, "account", "", "system service account override (must already exist)")
	service := &cobra.Command{Use: "service", Short: "Install or maintain the native supervisor integration"}
	for _, action := range []string{"render", "install", "update", "uninstall"} {
		var binary, account string
		c := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := platformpath.Resolve(scope)
			if err != nil {
				return err
			}
			switch action {
			case "render":
				s, err := localadmin.Render(l, account)
				if err != nil {
					return err
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), s)
				return err
			case "uninstall":
				return localadmin.Uninstall(cmd.Context(), l)
			default:
				return localadmin.Install(cmd.Context(), l, binary, account, action == "update")
			}
		}}
		c.Flags().StringVar(&binary, "binary", "", "candidate cpra executable to copy into the managed installation")
		c.Flags().StringVar(&account, "account", "", "system service account override (must already exist)")
		service.AddCommand(c)
	}
	var dataDir, output, config string
	backup := &cobra.Command{Use: "backup", Args: cobra.NoArgs, Short: "Verify and copy a stopped complete durable store", RunE: func(cmd *cobra.Command, _ []string) error { return localadmin.Backup(dataDir, output, config) }}
	backup.Flags().StringVar(&dataDir, "data-dir", "", "existing stopped state directory")
	backup.Flags().StringVar(&output, "output", "", "new backup directory (parent must exist)")
	backup.Flags().StringVar(&config, "config", "", "matching monitor configuration to fingerprint; credentials are not copied")
	var restoreDir, backupDir string
	restore := &cobra.Command{Use: "restore", Args: cobra.NoArgs, Short: "Verify a backup and restore into a new state directory", RunE: func(cmd *cobra.Command, _ []string) error { return localadmin.Restore(backupDir, restoreDir) }}
	restore.Flags().StringVar(&restoreDir, "data-dir", "", "new destination state directory")
	restore.Flags().StringVar(&backupDir, "backup", "", "complete backup directory")
	root.AddCommand(paths, init, service, backup, restore)
	return root
}
