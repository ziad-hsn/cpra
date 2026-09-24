package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
	if s.mutationReady() || !s.admissionStopped() {
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
	f.server.cfg.Ready = ready.Load
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
	s := &Server{cfg: ServerConfig{Ready: ready.Load}}
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
