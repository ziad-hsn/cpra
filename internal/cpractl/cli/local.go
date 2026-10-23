package cli

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/localadmin"
)

func newLocalCommand() *cobra.Command {
	var scope string
	root := &cobra.Command{Use: "local", Short: "Manage local installation, access, encryption, and stopped backups"}
	root.PersistentFlags().StringVar(&scope, "scope", "user", "installation scope: user or system")
	paths := &cobra.Command{Use: "paths", Args: cobra.NoArgs, Short: "Show native configuration and state paths", RunE: func(cmd *cobra.Command, _ []string) error {
		l, err := installpath.Resolve(scope)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(l)
	}}
	var initAccount string
	var management bool
	var initPort int
	var operator, tokenOutput string
	init := &cobra.Command{Use: "init", Args: cobra.NoArgs, Short: "Create absent starter files without replacing existing configuration", RunE: func(cmd *cobra.Command, _ []string) error {
		if initPort < 1 || initPort > 65535 {
			return fmt.Errorf("--port must be from 1 through 65535")
		}
		l, err := installpath.Resolve(scope)
		if err != nil {
			return err
		}
		if management {
			files, err := localadmin.InitManagement(cmd.Context(), l, localadmin.ManagementInitOptions{Account: initAccount, OperatorID: operator, TokenOutput: tokenOutput, Port: initPort})
			if err != nil {
				return err
			}
			quote := func(s string) string {
				if runtime.GOOS == "windows" {
					return "'" + strings.ReplaceAll(s, "'", "''") + "'"
				}
				return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
			}
			origin := fmt.Sprintf("https://localhost:%d", initPort)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Management configuration initialized at %s\nOperator token saved in %s (value not printed).\n\nStart:\ncpra -yaml %s -runtime-config %s -allow-empty\n\nConnect:\ncpractl --server %s --ca-file %s --token-file %s get monitors\n\nBrowser: %s\nManually trust the local CA %s in your browser before signing in. Initialization does not install trust or start a service.\n", l.ConfigDir, files.Token, quote(files.Manifest), quote(files.Runtime), origin, quote(files.CA), quote(files.Token), origin, files.CA)
			return err
		}
		if tokenOutput != "" || operator != "local/operator" {
			return fmt.Errorf("--token-output and --operator require --management")
		}
		port := 0
		if cmd.Flags().Changed("port") {
			port = initPort
		}
		if err = localadmin.Init(l, initAccount, port); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Configuration initialized at", l.ConfigDir)
		return nil
	}}
	init.Flags().StringVar(&initAccount, "account", "", "system service account override (must already exist)")
	init.Flags().BoolVar(&management, "management", false, "initialize protected named access and localhost TLS for a fresh installation")
	init.Flags().IntVar(&initPort, "port", 8060, "loopback API port saved in runtime configuration and used by generated services")
	init.Flags().StringVar(&operator, "operator", "local/operator", "named management operator identity")
	init.Flags().StringVar(&tokenOutput, "token-output", "", "protected operator token output outside service-owned paths; required for system management initialization")
	service := &cobra.Command{Use: "service", Short: "Install or maintain the native supervisor integration"}
	for _, action := range []string{"render", "install", "update", "uninstall"} {
		var binary, account, serviceBackupKey string
		descriptions := map[string]string{
			"render":    "Print the native service definition without installing or starting it",
			"install":   "Install an administrator-managed executable and native service definition",
			"update":    "Back up stopped state and replace a locally managed installation",
			"uninstall": "Remove a locally managed service while preserving configuration and state",
		}
		c := &cobra.Command{Use: action, Short: descriptions[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := installpath.Resolve(scope)
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
				return localadmin.Install(cmd.Context(), l, binary, account, action == "update", serviceBackupKey)
			}
		}}
		c.Flags().StringVar(&binary, "binary", "", "candidate cpra executable to copy into the managed installation")
		c.Flags().StringVar(&account, "account", "", "system service account override (must already exist)")
		if action == "update" {
			c.Flags().StringVar(&serviceBackupKey, "backup-auth-key", "", "protected external 32-byte key for the mandatory pre-update backup")
		}
		service.AddCommand(c)
	}
	var dataDir, output, config, backupKey string
	backup := &cobra.Command{Use: "backup", Args: cobra.NoArgs, Short: "Verify and copy a stopped complete durable store", RunE: func(cmd *cobra.Command, _ []string) error {
		return localadmin.Backup(dataDir, output, config, backupKey)
	}}
	backup.Flags().StringVar(&dataDir, "data-dir", "", "existing stopped state directory")
	backup.Flags().StringVar(&output, "output", "", "new backup directory (parent must exist)")
	backup.Flags().StringVar(&config, "config", "", "matching monitor configuration to fingerprint; credentials are not copied")
	backup.Flags().StringVar(&backupKey, "backup-auth-key", "", "protected external 32-byte backup authentication key file")
	var restoreDir, backupDir, restoreKey string
	restore := &cobra.Command{Use: "restore", Args: cobra.NoArgs, Short: "Verify a backup and restore into a new state directory", RunE: func(cmd *cobra.Command, _ []string) error {
		return localadmin.Restore(backupDir, restoreDir, restoreKey)
	}}
	restore.Flags().StringVar(&restoreDir, "data-dir", "", "new destination state directory")
	restore.Flags().StringVar(&backupDir, "backup", "", "complete backup directory")
	restore.Flags().StringVar(&restoreKey, "backup-auth-key", "", "protected external key used to authenticate this backup")
	root.AddCommand(paths, init, service, backup, restore, newLocalAuthCommand(), newLocalEncryptionCommand(), newLocalActionsCommand(), newLocalReviewCommand(), newLocalBootstrapCommand())
	registerLocalExtensions(root)
	return root
}
