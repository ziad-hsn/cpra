//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCpractlDiffSignalHelper(t *testing.T) {
	if os.Getenv("CPRA_DIFF_SIGNAL_HELPER") != "1" {
		return
	}
	os.Args = []string{"cpractl", "diff", "-f", "-", "-o", "json"}
	main()
	t.Fatal("incomplete standard input unexpectedly completed")
}

// The child runs the actual main entrypoint with a native stdin pipe whose
// writer remains open. Signals must unblock the command long enough to remove
// plaintext staging, then return diff's failure status.
func TestCpractlDiffSignalsRemoveBlockedStdinSpool(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCpractlDiffSignalHelper$")
			child.Env = append(os.Environ(), "CPRA_DIFF_SIGNAL_HELPER=1", "TMPDIR="+dir, "CPRA_SERVER=http://127.0.0.1:1", "CPRA_AUTH_TOKEN=", "CPRA_AUTH_TOKEN_FILE=", "CPRA_CA_FILE=")
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			// Incomplete input proves source EOF was not reached. Once the source
			// file contains this byte, Freeze is awaiting more bytes from stdin.
			if _, err := stdin.Write([]byte("{")); err != nil {
				t.Fatal(err)
			}
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			poll := time.NewTicker(10 * time.Millisecond)
			defer poll.Stop()
			ready := false
			for !ready {
				select {
				case err := <-done:
					t.Fatalf("helper exited before input freeze: %v %s", err, stderr.String())
				case <-deadline.C:
					t.Fatal("helper did not create source staging")
				case <-poll.C:
					matches, err := filepath.Glob(filepath.Join(dir, "cpra-collection-*", "source"))
					if err != nil {
						t.Fatal(err)
					}
					for _, path := range matches {
						info, err := os.Stat(path)
						if err == nil && info.Size() > 0 {
							ready = true
						}
					}
				}
			}
			if err := child.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "canceled") {
					t.Fatalf("signal did not produce bounded diff cancellation: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("signal left the native stdin read blocking")
			}
			matches, err := filepath.Glob(filepath.Join(dir, "cpra-collection-*"))
			if err != nil || len(matches) != 0 {
				t.Fatal("signal left private collection staging")
			}
		})
	}
}
