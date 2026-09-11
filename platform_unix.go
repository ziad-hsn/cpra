//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func runPlatform(run func(context.Context, func()) error) error {
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Restore the default signal behavior after the first signal so an operator
	// can interrupt an uncooperative shutdown with a second signal.
	restore := context.AfterFunc(signalCtx, stop)
	defer restore()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	var notifyErr error
	err := run(ctx, func() {
		notifyErr = notifySystemdReady()
		if notifyErr != nil {
			cancel()
		}
	})
	return errors.Join(err, notifyErr)
}

// No socket means a console/launchd run. Only the main process sends readiness.
func notifySystemdReady() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("systemd readiness notification: %w", err)
	}
	defer c.Close()
	if err = c.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err = c.Write([]byte("READY=1"))
	return err
}
