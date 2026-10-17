//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPlatformHangupDoesNotReloadOrStop(t *testing.T) {
	if os.Getenv("CPRA_TEST_SIGNAL_CHILD") == "1" {
		err := runPlatform(func(ctx context.Context, _ func()) error {
			fmt.Println("signal-child-ready")
			<-ctx.Done()
			fmt.Println("signal-child-draining")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			fmt.Println("signal-child-drained")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPlatformHangupDoesNotReloadOrStop$")
	cmd.Env = append(os.Environ(), "CPRA_TEST_SIGNAL_CHILD=1", "NOTIFY_SOCKET=", "CPRA_ENV=production", "CPRA_LOG_LEVEL=info")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	waitLine := func(want string) {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("process exited before %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("missing %q: %v", want, ctx.Err())
			}
		}
	}
	waitLine("signal-child-ready")
	for range 2 {
		if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		waitLine("SIGHUP does not reload configuration")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitLine("signal-child-draining")
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitLine("SIGHUP does not reload configuration")
	if _, err := fmt.Fprintln(input, "complete drain"); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	waitLine("signal-child-drained")
	if err := cmd.Wait(); err != nil {
		t.Fatal("graceful termination failed", err)
	}
}
