package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagementAdmissionBarrierJoinsAcceptedCallbacks(t *testing.T) {
	s := &Server{}
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		completed <- s.withMutationAdmission(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.StopAdmission(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("barrier returned before accepted callback completed", err)
	}
	if s.mutationReady(t.Context()) || !s.admissionStopped() {
		t.Fatal("deadline reopened write admission")
	}
	var invoked bool
	if err := s.withMutationAdmission(context.Background(), func() error { invoked = true; return nil }); err == nil || invoked {
		t.Fatal("new write crossed stopped barrier", err)
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if err := s.StopAdmission(context.Background()); err != nil {
		t.Fatal("repeated barrier did not join original callback", err)
	}
}

func TestManagementReadinessAndDrainingRejectWritesKeepReads(t *testing.T) {
	f := newManagementFixture(t, true)
	var ready atomic.Bool
	f.server.cfg.Ready = func(context.Context) bool { return ready.Load() }
	body := []byte(`{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"during-drain"},"spec":{"displayName":"Fixture contact"}}`)
	response, raw := f.request(t, http.MethodPost, "/api/v2/recipients", managementOperatorToken, body, map[string]string{"If-None-Match": "*"})
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "admissionUnavailable") || response.Header.Get("X-Operation-ID") != "" {
		t.Fatal("unready write admitted", response.StatusCode, string(raw))
	}
	ready.Store(true)
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, raw = f.request(t, http.MethodPost, "/api/v2/recipients", managementOperatorToken, body, map[string]string{"If-None-Match": "*"})
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "admissionUnavailable") || response.Header.Get("X-Operation-ID") != "" {
		t.Fatal("draining write admitted", response.StatusCode, string(raw))
	}
	response, raw = f.request(t, http.MethodGet, "/api/v2/recipients", managementReaderToken, nil, nil)
	if response.StatusCode != http.StatusOK || strings.Contains(string(raw), "during-drain") {
		t.Fatal("reads unavailable or rejected resource committed", response.StatusCode, string(raw))
	}
	response, _ = f.request(t, http.MethodGet, "/api/v1/readyz", managementReaderToken, nil, nil)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("drain did not make readiness unavailable", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodGet, "/api/v1/healthz", managementReaderToken, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatal("drain suppressed liveness diagnostics", response.StatusCode)
	}
}

func TestManagementCommitBoundaryRechecksReadinessAndCancellation(t *testing.T) {
	var ready atomic.Bool
	s := &Server{cfg: ServerConfig{Ready: func(context.Context) bool { return ready.Load() }}}
	var invoked bool
	if err := s.withMutationAdmission(context.Background(), func() error { invoked = true; return nil }); err == nil || invoked {
		t.Fatal("unready commit admitted", err)
	}
	ready.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.withMutationAdmission(ctx, func() error { invoked = true; return nil }); !errors.Is(err, context.Canceled) || invoked {
		t.Fatal("cancelled commit admitted", err)
	}
}

func TestManagementStopAdmissionDoesNotWaitForReadiness(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		name := "observation"
		if mutation {
			name = "mutation"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			s := &Server{cfg: ServerConfig{Ready: func(context.Context) bool {
				close(entered)
				<-release
				return true
			}}}
			type outcome struct {
				ready bool
				err   error
			}
			result := make(chan outcome, 1)
			requestDone, stopDone := make(chan struct{}), make(chan struct{})
			stopStarted := false
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				select {
				case <-requestDone:
				case <-time.After(time.Second):
					t.Error("request did not exit after readiness was released")
				}
				if stopStarted {
					select {
					case <-stopDone:
					case <-time.After(time.Second):
						t.Error("admission stop did not exit after readiness was released")
					}
				}
			})
			var committed atomic.Bool
			go func() {
				defer close(requestDone)
				if mutation {
					err := s.withMutationAdmission(context.Background(), func() error {
						committed.Store(true)
						return nil
					})
					result <- outcome{err: err}
				} else {
					result <- outcome{ready: s.mutationReady(t.Context())}
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request did not reach readiness")
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			stopped := make(chan error, 1)
			stopStarted = true
			go func() {
				defer close(stopDone)
				stopped <- s.StopAdmission(ctx)
			}()
			select {
			case err := <-stopped:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal("unexpected admission stop result", err)
				}
			case <-time.After(time.Second):
				t.Fatal("admission stop waited for the readiness callback")
			}
			if !s.admissionStopped() {
				t.Fatal("canceled stop did not close admission")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case got := <-result:
				if got.ready || mutation && got.err == nil || committed.Load() {
					t.Fatal("request crossed stopped admission after readiness returned", got)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not finish after readiness returned")
			}
		})
	}
}

func TestManagementAdmissionRechecksCancellationAfterReadiness(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	s := &Server{cfg: ServerConfig{Ready: func(context.Context) bool {
		close(entered)
		<-release
		return true
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("request did not exit after readiness was released")
		}
	})
	var committed atomic.Bool
	result := make(chan error, 1)
	go func() {
		defer close(done)
		result <- s.withMutationAdmission(ctx, func() error {
			committed.Store(true)
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach readiness")
	}
	cancel()
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || committed.Load() {
			t.Fatal("cancellation during readiness admitted a commit", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish after readiness returned")
	}
	if s.admissionStopped() {
		t.Fatal("request cancellation stopped global admission")
	}
}

func TestManagementReadinessDeadlineDoesNotCloseAdmission(t *testing.T) {
	release := make(chan struct{})
	s := &Server{cfg: ServerConfig{ReadTimeout: 20 * time.Millisecond, Ready: func(ctx context.Context) bool {
		select {
		case <-ctx.Done():
		case <-release:
		}
		return false
	}}}
	var committed atomic.Bool
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() { close(release); <-finished })
	go func() {
		defer close(finished)
		done <- s.withMutationAdmission(t.Context(), func() error { committed.Store(true); return nil })
	}()
	select {
	case err := <-done:
		if err == nil || committed.Load() || s.admissionStopped() {
			t.Fatal("readiness timeout admitted a write or stopped global admission", err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness ignored its configured observation budget")
	}
	s.cfg.Ready = func(ctx context.Context) bool { return ctx.Err() == nil }
	if err := s.withMutationAdmission(t.Context(), func() error { committed.Store(true); return nil }); err != nil || !committed.Load() {
		t.Fatal("readiness timeout prevented a later healthy write", err)
	}
}
