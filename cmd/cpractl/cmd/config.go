package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"cpra/cmd/cpractl/config"
)

var setContextServer string

func init() {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage cpractl configuration",
	}

	viewCmd := &cobra.Command{
		Use:   "view",
		Short: "Show current configuration",
		RunE:  runConfigView,
	}

	useContextCmd := &cobra.Command{
		Use:   "use-context <name>",
		Short: "Switch to a named context",
		Args:  cobra.ExactArgs(1),
		RunE:  runUseContext,
	}

	setContextCmd := &cobra.Command{
		Use:   "set-context <name>",
		Short: "Create or update a named context",
		Args:  cobra.ExactArgs(1),
		RunE:  runSetContext,
	}
	setContextCmd.Flags().StringVar(&setContextServer, "server", "", "Server address for the context")

	configCmd.AddCommand(viewCmd, useContextCmd, setContextCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigView(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func runUseContext(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if _, ok := cfg.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found", name)
	}

	cfg.CurrentContext = name
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("Switched to context %q\n", name)
	return nil
}

func runSetContext(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx := cfg.Contexts[name]
	if setContextServer != "" {
		ctx.Server = setContextServer
	}
	cfg.Contexts[name] = ctx

	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("Context %q saved\n", name)
	return nil
}

func loadConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFrom(cfgFile)
	}
	return config.Load()
}

func saveConfig(cfg *config.Config) error {
	if cfgFile != "" {
		return config.SaveTo(cfgFile, cfg)
	}
	return config.Save(cfg)
}
