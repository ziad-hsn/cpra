// Command cpractl is the command-line interface for the CPRA monitoring
// platform. It uses the public management SDK and explicit legacy read client.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ziad-hsn/cpra/internal/cpractl/cli"
)

func main() {
	root := cli.NewRootCommand()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // Restore the default action so a second signal can force exit.
	}()
	err := root.ExecuteContext(ctx)
	stop()
	if err != nil {
		code := cli.ExitCode(err)
		if code != 1 || !errors.Is(err, cli.ErrDifferences) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(code)
	}
}
