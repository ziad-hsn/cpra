package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	cpractlconfig "cpra/cmd/cpractl/config"
)

var cfgFile string

// rootCmd is the base command for cpractl
var rootCmd = &cobra.Command{
	Use:   "cpractl",
	Short: "CPRA monitoring system CLI",
	Long:  `cpractl provides command-line access to CPRA's monitoring and remediation features`,
}

// Execute is the entry point called by main
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default $HOME/.cpractl/config.yaml)")
	rootCmd.PersistentFlags().StringP("server", "s", "http://localhost:8080", "CPRA server address")
	rootCmd.PersistentFlags().StringP("output", "o", "table", "Output format (table|json|yaml|wide)")
	rootCmd.PersistentFlags().Duration("timeout", 30*time.Second, "Request timeout")
	rootCmd.PersistentFlags().Bool("insecure", false, "Skip TLS verification")
	rootCmd.PersistentFlags().String("context", "default", "Named context to use from config file")

	// Bind flags to Viper
	viper.BindPFlags(rootCmd.PersistentFlags())

	// Environment variables prefix
	viper.SetEnvPrefix("CPRACTL")
	viper.AutomaticEnv()
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			viper.AddConfigPath(filepath.Join(home, ".cpractl"))
		}
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}

	// Ignore errors if config file doesn't exist
	_ = viper.ReadInConfig()

	applyConfigOverrides()
}

func applyConfigOverrides() {
	var (
		cfg *cpractlconfig.Config
		err error
	)
	if cfgFile != "" {
		cfg, err = cpractlconfig.LoadFrom(cfgFile)
	} else {
		cfg, err = cpractlconfig.Load()
	}
	if err != nil {
		return
	}

	contextName := cfg.CurrentContext
	contextFlagSet := rootCmd.PersistentFlags().Changed("context")
	contextEnvSet := envSet("CPRACTL_CONTEXT")
	if contextFlagSet || contextEnvSet {
		if v := viper.GetString("context"); v != "" {
			contextName = v
		}
	}

	ctx, ok := cfg.Contexts[contextName]
	if !ok {
		return
	}

	serverFlagSet := rootCmd.PersistentFlags().Changed("server")
	serverEnvSet := envSet("CPRACTL_SERVER")
	if !serverFlagSet && !serverEnvSet && ctx.Server != "" {
		viper.Set("server", ctx.Server)
	}

	insecureFlagSet := rootCmd.PersistentFlags().Changed("insecure")
	insecureEnvSet := envSet("CPRACTL_INSECURE")
	if !insecureFlagSet && !insecureEnvSet {
		viper.Set("insecure", ctx.Insecure)
	}

	if !contextFlagSet && !contextEnvSet && contextName != "" {
		viper.Set("context", contextName)
	}
}

func envSet(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}
