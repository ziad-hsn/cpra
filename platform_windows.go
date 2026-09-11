package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
)

type windowsService struct {
	run func(context.Context, func()) error
	err error
}

func runPlatform(run func(context.Context, func()) error) error {
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !service {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		return run(ctx, func() {})
	}
	s := &windowsService{run: run}
	if err := svc.Run("CPRa", s); err != nil {
		return err
	}
	return s.err
}

func (s *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), shutdownLimitKey{}, 15*time.Second))
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	status := svc.Status{State: svc.StartPending, WaitHint: 120000, CheckPoint: 1}
	changes <- status
	go func() { done <- s.run(ctx, func() { once.Do(func() { close(ready) }) }) }()
	// Large manifests and snapshot restoration can exceed the initial wait
	// hint. Tell SCM that the process remains in its bounded startup/shutdown
	// path; only the application callback can report Running.
	progress := time.NewTicker(5 * time.Second)
	defer progress.Stop()
	stopping := false
	for {
		select {
		case <-ready:
			ready = nil
			if !stopping {
				status = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
				changes <- status
			}
		case request, ok := <-requests:
			if !ok {
				stopping = true
				cancel()
				requests = nil
				continue
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- status
			case svc.Stop, svc.Shutdown:
				stopping = true
				status = svc.Status{State: svc.StopPending, WaitHint: 15000, CheckPoint: 1}
				changes <- status
				cancel()
			}
		case <-progress.C:
			if status.State == svc.StartPending || status.State == svc.StopPending {
				status.CheckPoint++
				changes <- status
			}
		case err := <-done:
			s.err = err
			if err != nil {
				s.err = fmt.Errorf("service runtime: %w", err)
				return true, 1
			}
			return false, 0
		}
	}
}
