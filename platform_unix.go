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

	"github.com/ziad-hsn/cpra/internal/logger"
)

func runPlatform(run func(context.Context, func()) error) error {
	log, err := logger.NewLoggerWithComponent("signals")
	if err != nil {
		return err
	}
	defer log.Sync()
	stopReloadNotice := watchReloadSignal(func() {
		log.Warn("SIGHUP does not reload configuration; use cpractl apply in management mode, or stop and restart a file-configured instance")
	})
	defer stopReloadNotice()
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Restore the default signal behavior after the first signal so an operator
	// can interrupt an uncooperative shutdown with a second signal.
	restore := context.AfterFunc(signalCtx, stop)
	defer restore()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	var notifyErr error
	err = run(ctx, func() {
		notifyErr = notifySystemdReady()
		if notifyErr != nil {
			cancel()
		}
	})
	return errors.Join(err, notifyErr)
}

// Keep SIGHUP handled throughout draining as well as normal operation. Signal
// delivery is coalesced; it never reads configuration or starts a new owner.
func watchReloadSignal(notice func()) func() {
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-reload:
				notice()
			}
		}
	}()
	return func() {
		signal.Stop(reload)
		close(stop)
		<-done
	}
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
