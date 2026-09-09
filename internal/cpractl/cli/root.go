// Package cli implements the cpractl command tree. It is a thin frontend over
// the typed API client in internal/client: it builds Cobra commands, parses
// flags, calls the client, and renders the result in the requested output
// format. It contains no HTTP logic of its own.
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cpra/internal/client"
	"cpra/internal/version"
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

	apiClient *client.Client
}

// mustClient returns the API client, constructing it on first use. Commands
// that do not talk to the server (completion, help) never call this, so a
// malformed --server value does not break them.
func (o *options) mustClient() (*client.Client, error) {
	if o.apiClient != nil {
		return o.apiClient, nil
	}
	token := os.Getenv("CPRA_AUTH_TOKEN")
	if o.tokenFile != "" {
		data, err := os.ReadFile(o.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read auth token file: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return nil, fmt.Errorf("auth token file is empty")
		}
	}
	c, err := client.New(client.Config{BaseURL: o.server, Timeout: o.timeout, AuthToken: token})
	if err != nil {
		return nil, err
	}
	o.apiClient = c
	return c, nil
}

// NewRootCommand builds the cpractl command tree.
func NewRootCommand() *cobra.Command {
	o := &options{}

	root := &cobra.Command{
		Use:   "cpractl",
		Short: "Inspect CPRa monitors and runtime state",
		Long: "cpractl queries the CPRA web server's read-only API to inspect " +
			"monitors, incidents, queues, worker pools, and runtime config. " +
			"Point it at a server with --server or the CPRA_SERVER environment variable.",
		Version:       version.Info(),
		SilenceUsage:  true, // don't print usage on every error
		SilenceErrors: true, // errors are printed by main
	}

	root.PersistentFlags().StringVar(&o.server, "server", envOr("CPRA_SERVER", "http://localhost:8060"),
		"CPRA server address (env CPRA_SERVER)")
	root.PersistentFlags().StringVar(&o.tokenFile, "token-file", os.Getenv("CPRA_AUTH_TOKEN_FILE"), "file containing the API token (or use CPRA_AUTH_TOKEN)")
	root.PersistentFlags().DurationVar(&o.timeout, "request-timeout", 10*time.Second,
		"per-request timeout")
	root.PersistentFlags().StringVarP(&o.output, "output", "o", formatTable,
		"output format: table|wide|json|yaml")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch o.output {
		case formatTable, formatWide, formatJSON, formatYAML:
			return nil
		default:
			return fmt.Errorf("invalid output format %q: must be one of table|wide|json|yaml", o.output)
		}
	}

	root.AddCommand(
		newGetCommand(o),
		newHealthCommand(o),
		newMetricsCommand(o),
	)
	return root
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
