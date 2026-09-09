// Command cpractl is the command-line interface for the CPRA monitoring
// platform. It talks to the CPRA web server's read-only API via the typed
// client in internal/client.
package main

import (
	"fmt"
	"os"

	"cpra/internal/cpractl/cli"
)

func main() {
	root := cli.NewRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
