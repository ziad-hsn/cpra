//go:build externaljobs

package httpserver

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/controller"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Reuse the fixture certificate and trust roots, then exercise Server.Start's
// listener, middleware, routes and timeouts. No protocol transport is replaced.
func startWorkerProductionServer(t *testing.T, f *workerSessionFixture, enabled bool, ready func(context.Context) bool) (*Server, *cpra.WorkerClient, *http.Client) {
	t.Helper()
	if controller.SystemLogger == nil {
		controller.SystemLogger = controller.NewLogger("worker-http-test", false)
		t.Cleanup(func() { controller.SystemLogger = nil })
	}
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	httpClient := certificateServer.Client()
	certificate := certificateServer.TLS.Certificates[0]
	certificateServer.Close()
	directory := t.TempDir()
	certFile, keyFile := filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key")
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{Addr: "127.0.0.1:0", Store: f.store, Management: f.handler.catalog, ManagementAuth: f.handler.auth,
		TLSCertFile: certFile, TLSKeyFile: keyFile, Ready: ready, AllowEmpty: true}
	cfg.ExternalJobsEnabled = enabled
	s := New(cfg, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.StopContext(ctx); err != nil {
			t.Error("server did not join", err)
		}
	})
	client, err := cpra.NewWorkerClient(cpra.Config{BaseURL: "https://" + s.srv.Addr, AuthToken: f.token, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return s, client, httpClient
}

func workerRouteStatus(t *testing.T, s *Server, client *http.Client, path, token string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+s.srv.Addr+path, bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
	return response.StatusCode
}

func TestWorkerProductionRoutesRequireOptInAndWorkerAuthority(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, enabled := range []bool{false, true} {
		s, _, client := startWorkerProductionServer(t, f, enabled, nil)
		for _, operation := range []string{"poll", "start", "result", "heartbeat", "late-evidence"} {
			path := "/api/v2/external-workers/" + operation
			for _, token := range []string{managementOperatorToken, managementReaderToken, managementLegacyToken} {
				want := http.StatusNotFound
				if enabled {
					want = http.StatusUnauthorized
				}
				if status := workerRouteStatus(t, s, client, path, token); status != want {
					t.Fatal("worker route accepted an operator/reader/bootstrap credential", enabled, path, status, want)
				}
			}
		}
	}
}

func TestWorkerProductionDrainRetainsResultsAndEvidence(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			f := newWorkerSessionFixtureForCategory(t, category)
			if category == "check" {
				queueWorkerHTTPCheck(t, f)
			} else {
				queueWorkerHTTPAction(t, f, category)
			}
			var ready atomic.Bool
			ready.Store(true)
			s, client, _ := startWorkerProductionServer(t, f, true, func(context.Context) bool { return ready.Load() })
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 {
				t.Fatal("production offer", err)
			}
			a := offers.Items[0]
			start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("production grant", err)
			}
			if err := s.StopAdmission(t.Context()); err != nil {
				t.Fatal(err)
			}
			ready.Store(false) // Controller/collection drain must not discard outcomes.
			if s.mutationReady(t.Context()) {
				t.Fatal("draining server reports admission ready")
			}
			if _, err := client.Poll(t.Context(), f.request); err == nil {
				t.Fatal("poll crossed the new-work barrier")
			}
			if _, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin)); err == nil {
				t.Fatal("begin crossed the new-work barrier")
			}
			reconciled, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeReconcile))
			if err != nil || reconciled.Disposition != api.StartDispositionStarted {
				t.Fatal("safe reconciliation was rejected during drain", err)
			}
			h, err := client.Heartbeat(t.Context(), api.HeartbeatRequest{ServerID: a.ServerID, SessionID: a.SessionID, WorkerID: f.request.WorkerID, ExecutionID: a.ExecutionID, GrantID: start.GrantID})
			if err != nil || !h.Accepted {
				t.Fatal("original execution heartbeat was rejected", err)
			}
			status := "unknown"
			if category == "check" {
				status = "noData"
			}
			outcome := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: start.GrantID, Kind: category, Status: status, Evidence: []string{}}
			receipt, err := client.Result(t.Context(), outcome)
			want := api.ReceiptDispositionUnknown
			if category == "check" {
				want = api.ReceiptDispositionFinalized
			}
			if err != nil || receipt == nil || receipt.Disposition != want {
				t.Fatal("outcome lost during drain", err)
			}
			if replay, err := client.Result(t.Context(), outcome); err != nil || !reflect.DeepEqual(receipt, replay) {
				t.Fatal("draining replay changed the original receipt", err)
			}
			before, _, _ := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			input := api.LateEvidenceRequest{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID,
				EvidenceID: uuid.NewString(), OriginalReceiptID: receipt.ReceiptID, Evidence: []string{"private-drain-evidence"}, Data: json.RawMessage(`"private-schema-canary"`)}
			evidence, err := client.LateEvidence(t.Context(), input)
			if err != nil || evidence.Disposition != api.ReceiptDispositionAccepted {
				t.Fatal("evidence lost during drain", err)
			}
			after, _, _ := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			if !reflect.DeepEqual(before.Result, after.Result) || len(after.Evidence) != 1 {
				t.Fatal("late evidence changed original outcome")
			}
			if category != "check" {
				monitor, _ := f.store.Get(a.MonitorID)
				if monitor.Actions[after.Intent.ActionID].State != persistence.Unknown {
					t.Fatal("drain or late evidence released uncertain action")
				}
			}
			if err := s.StopContext(t.Context()); err != nil {
				t.Fatal("completion barrier did not join", err)
			}
			if err := f.store.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.workerHTTP.completions.admit(t.Context(), func() error { t.Error("completion reopened"); return nil }); err == nil {
				t.Fatal("completion admission reopened after shutdown")
			}
		})
	}
}

func TestWorkerProductionDrainCommitsKnownOutcomes(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			f := newWorkerSessionFixtureForCategory(t, category)
			if category == "check" {
				queueWorkerHTTPCheck(t, f)
			} else {
				queueWorkerHTTPAction(t, f, category)
			}
			s, client, _ := startWorkerProductionServer(t, f, true, nil)
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 {
				t.Fatal("offer", err)
			}
			a := offers.Items[0]
			grant, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || grant.Disposition != api.StartDispositionGranted {
				t.Fatal("start", err)
			}
			if err := s.StopAdmission(t.Context()); err != nil {
				t.Fatal(err)
			}
			status := map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category]
			outcome := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: grant.GrantID,
				Kind: category, Status: status, Evidence: []string{"fixture-completion-observed"}, Data: json.RawMessage(`"private-schema-canary"`)}
			receipt, err := client.Result(t.Context(), outcome)
			if err != nil || receipt == nil || receipt.Disposition != api.ReceiptDispositionAccepted {
				t.Fatal("known outcome discarded during drain", err)
			}
			record, _, _ := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			monitor, _ := f.store.Get(a.MonitorID)
			if record.Result == nil || record.Result.Input.Status != status || record.Result.Receipt.ReceiptID != receipt.ReceiptID {
				t.Fatal("completion reply preceded the durable result")
			}
			if category == "check" && (monitor.LastOutcome != "success" || monitor.TotalChecks != 1) || category != "check" && monitor.Actions[record.Intent.ActionID].State != persistence.Succeeded {
				t.Fatal("committed completion did not update monitor/action state")
			}
		})
	}
}

func TestWorkerProductionShutdownDeadlineRetainsActiveCompletion(t *testing.T) {
	f := newWorkerSessionFixture(t)
	s, client, _ := startWorkerProductionServer(t, f, true, nil)
	_, outcome := startWorkerHTTPCheck(t, f, client)
	entered, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	complete := s.workerHTTP.complete
	s.workerHTTP.complete = func(ctx context.Context, fn func() error) error {
		defer close(callbackDone)
		close(entered)
		<-release
		return complete(ctx, fn)
	}
	requestDone := make(chan error, 1)
	go func() { _, err := client.Result(t.Context(), outcome); requestDone <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion never entered its barrier")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if err := s.StopContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("HTTP shutdown reported a join while completion remained active", err)
	}
	s.workerHTTP.completions.mu.Lock()
	active, stopped := s.workerHTTP.completions.active, s.workerHTTP.completions.stopped
	s.workerHTTP.completions.mu.Unlock()
	if active != 1 || !stopped || !f.store.Status().Ready {
		t.Fatal("deadline dropped callback ownership or closed durable state")
	}
	once.Do(func() { close(release) })
	select {
	case <-callbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("released completion did not finish")
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("closed HTTP connection falsely confirmed the outcome")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client request did not observe shutdown")
	}
	if err := s.StopContext(t.Context()); err != nil {
		t.Fatal("repeated shutdown did not join original callback", err)
	}
	if err := f.store.Flush(t.Context()); err != nil {
		t.Fatal("joined state could not be flushed", err)
	}
}

func TestWorkerProductionShutdownRetainsPreparingRequest(t *testing.T) {
	f := newWorkerSessionFixture(t)
	s, client, _ := startWorkerProductionServer(t, f, true, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	// A canceled HTTP connection cannot prove that code using storage or keys
	// has returned. Model preparation that has not reached the commit callback.
	s.workerHTTP.ready = func(ctx context.Context) error {
		close(entered)
		<-release
		return ctx.Err()
	}
	completed := make(chan error, 1)
	go func() { _, err := client.Poll(t.Context(), f.request); completed <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never entered preparation")
	}
	for attempt := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		err := s.StopContext(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("shutdown attempt %d reported a join before preparation returned: %v", attempt+1, err)
		}
	}
	s.workerHTTP.completions.mu.Lock()
	active := s.workerHTTP.completions.active
	s.workerHTTP.completions.mu.Unlock()
	if active != 1 || !f.store.Status().Ready {
		t.Error("preparation lost dependency ownership after the connection closed")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-completed:
		if err == nil {
			t.Error("closed connection confirmed a poll")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not observe the closed connection")
	}
	if err := s.StopContext(t.Context()); err != nil {
		t.Fatal("released request did not join", err)
	}
}

func TestWorkerProductionStopCancelsWaitingPoll(t *testing.T) {
	f := newWorkerSessionFixture(t)
	s, client, _ := startWorkerProductionServer(t, f, true, nil)
	observed := &observedPollStore{Store: f.store, entered: make(chan time.Duration, 1)}
	s.workerHTTP.store = observed // Instrument a read; use the real durable wait.
	f.request.WaitSeconds = 25
	completed := make(chan error, 1)
	go func() { _, err := client.Poll(t.Context(), f.request); completed <- err }()
	select {
	case <-observed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("poll never waited")
	}
	if err := s.StopAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("stopped poll created a session")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopped poll retained its wait until the remote deadline")
	}
	s.workerHTTP.mu.Lock()
	polls := len(s.workerHTTP.polls)
	s.workerHTTP.mu.Unlock()
	if polls != 0 {
		t.Fatal("stopped poll retained admission capacity")
	}
}

func TestWorkerProductionMaximumPollFitsSocketTimeout(t *testing.T) {
	f := newWorkerSessionFixture(t)
	s, client, _ := startWorkerProductionServer(t, f, true, nil)
	f.request.WaitSeconds = 25
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	reply, err := client.Poll(ctx, f.request)
	if err != nil || reply == nil || len(reply.Items) != 0 || time.Since(start) < 25*time.Second {
		t.Fatal("production maximum poll did not complete its wait", err)
	}
	if s.srv.WriteTimeout != workerPollWriteTimeout {
		t.Fatal("production listener lost the poll response allowance")
	}
	s.cfg.WriteTimeout = 30 * time.Second
	if err := s.validateExtensions(); err == nil {
		t.Fatal("explicit insufficient socket timeout accepted")
	}
}

func TestWorkerCompletionDrainRetainsActiveOwnership(t *testing.T) {
	var drain workerCompletionDrain
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	go func() {
		completed <- drain.admit(t.Context(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := drain.stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown reported completion while the callback owned dependencies", err)
	}
	if err := drain.admit(t.Context(), func() error { t.Error("new completion admitted"); return nil }); err == nil {
		t.Fatal("canceled shutdown reopened completion admission")
	}
	once.Do(func() { close(release) })
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if err := drain.stop(t.Context()); err != nil {
		t.Fatal("later wait did not join the original callback", err)
	}
}
