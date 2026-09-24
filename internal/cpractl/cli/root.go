// Package cli implements the cpractl command tree. It is a thin frontend over
// the public SDK: it builds Cobra commands, parses
// flags, calls the client, and renders the result in the requested output
// format. It contains no HTTP logic of its own.
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ziad-hsn/cpra/internal/version"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

// Output format identifiers for the --output (-o) flag.
const (
	formatTable = "table"
	formatWide  = "wide"
	formatJSON  = "json"
	formatYAML  = "yaml"
)

// options holds the global flags shared by all subcommands, plus the lazily
// constructed API client.
type options struct {
	server    string
	tokenFile string
	timeout   time.Duration
	output    string

	apiClient    *cpra.Client
	caFile       string
	insecureHTTP bool
}

// NewRootCommand builds the cpractl command tree.
func NewRootCommand() *cobra.Command {
	o := &options{}

	root := &cobra.Command{
		Use:   "cpractl",
		Short: "Manage CPRa configuration and inspect runtime state",
		Long: "cpractl manages CPRa monitors, notification contacts, groups and secrets, " +
			"and inspects incidents, queues, worker pools and runtime configuration. " +
			"Point it at a server with --server or the CPRA_SERVER environment variable.",
		Version:       version.Info(),
		SilenceUsage:  true, // don't print usage on every error
		SilenceErrors: true, // errors are printed by main
	}

	root.PersistentFlags().StringVar(&o.server, "server", envOr("CPRA_SERVER", "http://localhost:8060"),
		"CPRA server address (env CPRA_SERVER)")
	root.PersistentFlags().StringVar(&o.tokenFile, "token-file", os.Getenv("CPRA_AUTH_TOKEN_FILE"), "file containing the API token (or use CPRA_AUTH_TOKEN)")
	root.PersistentFlags().StringVar(&o.caFile, "ca-file", os.Getenv("CPRA_CA_FILE"), "PEM trust roots for API HTTPS requests")
	root.PersistentFlags().BoolVar(&o.insecureHTTP, "allow-insecure-http", false, "explicitly allow API authentication over HTTP for the configured origin")
	root.PersistentFlags().DurationVar(&o.timeout, "request-timeout", 10*time.Second,
		"per-request timeout")
	root.PersistentFlags().StringVarP(&o.output, "output", "o", formatTable,
		"output format: table|wide|json|yaml")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch o.output {
		case formatTable, formatWide, formatJSON, formatYAML:
			return nil
		default:
			err := fmt.Errorf("invalid output format %q: must be one of table|wide|json|yaml", o.output)
			if cmd.Name() == "diff" {
				return diffFailure(&managementCommandError{"diff output must be one of table|wide|json|yaml", err})
			}
			return err
		}
	}

	get := newGetCommand(o)
	get.AddCommand(newGetUploadAttemptCommand(o))
	root.AddCommand(
		get,
		newHealthCommand(o),
		newReadyCommand(o),
		newLocalCommand(),
		newMetricsCommand(o),
		newDiffCommand(o),
		newApplyCommand(o),
		newResumeUploadCommand(o),
		newWaitCommand(o),
	)
	addManagementCommands(root, o)
	return root
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
