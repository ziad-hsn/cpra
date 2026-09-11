package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsServiceWaitsForReadinessAndDrainsOnStop(t *testing.T) {
	initialized := make(chan struct{})
	stopped := make(chan struct{})
	service := &windowsService{run: func(ctx context.Context, ready func()) error {
		if ctx.Value(shutdownLimitKey{}) != 15*time.Second {
			return errors.New("missing service shutdown budget")
		}
		<-initialized
		ready()
		<-ctx.Done()
		close(stopped)
		return nil
	}}
	requests := make(chan svc.ChangeRequest)
	changes := make(chan svc.Status, 8)
	done := make(chan uint32, 1)
	go func() {
		specific, code := service.Execute(nil, requests, changes)
		if specific && code == 0 {
			code = 99
		}
		done <- code
	}()
	if state := (<-changes).State; state != svc.StartPending {
		t.Fatal(state)
	}
	select {
	case got := <-changes:
		t.Fatalf("premature running: %+v", got)
	default:
	}
	close(initialized)
	select {
	case status := <-changes:
		if status.State != svc.Running || status.Accepts != svc.AcceptStop|svc.AcceptShutdown {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("service never ready")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if state := (<-changes).State; state != svc.StopPending {
		t.Fatal(state)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("service handler did not join runtime")
	}
	<-stopped
}
func TestWindowsServiceStartupFailureReturnsNonzero(t *testing.T) {
	service := &windowsService{run: func(context.Context, func()) error { return errors.New("storage inaccessible") }}
	changes := make(chan svc.Status, 3)
	specific, code := service.Execute(nil, make(chan svc.ChangeRequest), changes)
	if !specific || code == 0 || service.err == nil {
		t.Fatal("startup failure reported success")
	}
	for len(changes) > 0 {
		if (<-changes).State == svc.Running {
			t.Fatal("failed runtime reported running")
		}
	}
}

func TestWindowsServiceLongInitializationPublishesPendingProgress(t *testing.T) {
	service := &windowsService{run: func(ctx context.Context, _ func()) error {
		<-ctx.Done()
		return nil
	}}
	requests := make(chan svc.ChangeRequest)
	changes := make(chan svc.Status, 8)
	done := make(chan struct{})
	go func() { service.Execute(nil, requests, changes); close(done) }()
	defer func() {
		close(requests)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("service did not stop after the SCM request channel closed")
		}
	}()
	initial := <-changes
	select {
	case progress := <-changes:
		if progress.State != svc.StartPending || progress.CheckPoint <= initial.CheckPoint || progress.Accepts != 0 {
			t.Fatalf("startup heartbeat falsely reported readiness or no progress: %+v", progress)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("long initialization failed to update SCM startup progress")
	}
}
