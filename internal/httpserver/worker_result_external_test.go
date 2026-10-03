//go:build externaljobs

package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func startWorkerHTTPCheck(t *testing.T, f *workerSessionFixture, client *cpra.WorkerClient) (api.Assignment, api.Outcome) {
	t.Helper()
	queueWorkerHTTPCheck(t, f)
	offers, err := client.Poll(t.Context(), f.request)
	if err != nil || offers == nil || len(offers.Items) != 1 {
		t.Fatal("check offer", err)
	}
	a := offers.Items[0]
	start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
	if err != nil || start == nil || start.Disposition != api.StartDispositionGranted {
		t.Fatal("check grant", err)
	}
	return a, api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: start.GrantID,
		Kind: "check", Status: "success", Evidence: []string{"private-evidence-canary"}, Diagnostic: "private-diagnostic-canary", Data: json.RawMessage(`"private-schema-canary"`)}
}

func TestWorkerResultHTTPReceiptReplayAndConflict(t *testing.T) {
	f := newWorkerSessionFixture(t)
	client := workerHTTPClient(t, f)
	a, input := startWorkerHTTPCheck(t, f, client)
	before, _ := f.store.Get("monitor-one")
	receipt, err := client.Result(t.Context(), input)
	if err != nil || receipt == nil || receipt.Disposition != api.ReceiptDispositionAccepted {
		t.Fatal("result receipt", err)
	}
	saved, ok, err := f.store.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil || !ok || saved.Result == nil || saved.Result.Receipt.ReceiptID != receipt.ReceiptID {
		t.Fatal("receipt was not committed", err)
	}
	after, _ := f.store.Get("monitor-one")
	if after.TotalChecks != before.TotalChecks+1 || after.LastOutcome != "success" || after.Generation != saved.Intent.Generation || after.LatencyAvailable {
		t.Fatal("committed result did not preserve health/timing semantics")
	}
	for range 3 {
		replay, err := client.Result(t.Context(), input)
		if err != nil || !reflect.DeepEqual(receipt, replay) {
			t.Fatal("identical result lost the original receipt", err)
		}
	}
	for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
		terminal, err := client.Start(t.Context(), startWorkerOffer(a, mode))
		if err != nil || terminal.Disposition != api.StartDispositionTerminal || terminal.ReceiptID != receipt.ReceiptID || terminal.GrantID != "" || !terminal.Deadline.IsZero() {
			t.Fatal("completed execution returned an executable grant", err)
		}
	}
	changed := input
	changed.Diagnostic = "different-private-diagnostic-canary"
	if _, err = client.Result(t.Context(), changed); !errors.Is(err, cpra.ErrConflict) || errors.Is(err, cpra.ErrAmbiguous) {
		t.Fatal("contradictory outcome did not produce a definite conflict", err)
	}
	retained, _, _ := f.store.WorkerExecution(t.Context(), input.ExecutionID)
	unchanged, _ := f.store.Get("monitor-one")
	if !reflect.DeepEqual(retained, saved) || !reflect.DeepEqual(unchanged, after) {
		t.Fatal("replay/conflict changed original ciphertext or monitor")
	}
	for _, value := range []any{saved, receipt} {
		raw, err := json.Marshal(value)
		if err != nil || bytes.Contains(raw, []byte("canary")) {
			t.Fatal("result plaintext entered storage or receipt", err)
		}
	}
	if err := f.handler.catalog.Verify(t.Context()); err != nil {
		t.Fatal("retained encrypted outcome failed authentication", err)
	}
}

func TestWorkerResultHTTPLostReplyAndRestart(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "log"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			client := workerHTTPClient(t, f)
			a, input := startWorkerHTTPCheck(t, f, client)
			// Simulate a committed mutation whose response is lost. The next
			// request must recover its original result, not apply it again.
			var lost atomic.Bool
			f.handler.admit = func(ctx context.Context, fn func() error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := fn(); err != nil {
					return err
				}
				if lost.CompareAndSwap(false, true) {
					return persistence.ErrCommitUnconfirmed
				}
				return nil
			}
			if _, err := client.Result(t.Context(), input); !errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("lost receipt did not remain ambiguous", err)
			}
			saved, ok, err := f.store.WorkerExecution(t.Context(), input.ExecutionID)
			if err != nil || !ok || saved.Result == nil {
				t.Fatal("commit before response loss missing", err)
			}
			before, _ := f.store.Get("monitor-one")
			if snapshot {
				if err := f.store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := runtimeconfig.Default()
			cfg.Storage.Directory = f.dir
			store, err := persistence.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal("restart", err)
			}
			defer store.Close()
			keys, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
			if err != nil {
				t.Fatal(err)
			}
			sealer, err := secureconfig.NewSealer(keys)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := management.NewCatalog(store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			if err = catalog.Verify(t.Context()); err != nil {
				t.Fatal("restart result authentication", err)
			}
			f.handler.store, f.handler.catalog, f.handler.ready = store, catalog, store.ControllerHealthContext
			receipt, err := client.Result(t.Context(), input)
			if err != nil || receipt.ReceiptID != saved.Result.Receipt.ReceiptID || receipt.Disposition != api.ReceiptDispositionAccepted {
				t.Fatal("restart lost original committed receipt", err)
			}
			terminal, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeReconcile))
			if err != nil || terminal.Disposition != api.StartDispositionTerminal || terminal.ReceiptID != receipt.ReceiptID {
				t.Fatal("restart returned a grant for committed work", err)
			}
			retained, _, _ := store.WorkerExecution(t.Context(), input.ExecutionID)
			after, _ := store.Get("monitor-one")
			if !reflect.DeepEqual(saved, retained) || before.TotalChecks != after.TotalChecks || before.LastOutcome != after.LastOutcome || !before.LastCheck.Equal(after.LastCheck) {
				t.Fatal("replay changed original result or repeated the observation")
			}
		})
	}
}

func TestWorkerResultHTTPRejectsForeignIdentityAndContract(t *testing.T) {
	f := newWorkerSessionFixture(t)
	client := workerHTTPClient(t, f)
	_, input := startWorkerHTTPCheck(t, f, client)
	before, _, _ := f.store.WorkerExecution(t.Context(), input.ExecutionID)
	for name, change := range map[string]func(*api.Outcome){
		"server":    func(v *api.Outcome) { v.ServerID = "foreign-server" },
		"worker":    func(v *api.Outcome) { v.WorkerUID = "foreign-worker" },
		"execution": func(v *api.Outcome) { v.ExecutionID = "foreign-execution" },
		"grant":     func(v *api.Outcome) { v.GrantID = "foreign-grant" },
		"category":  func(v *api.Outcome) { v.Kind, v.Status = "recovery", "completed" },
		"schema":    func(v *api.Outcome) { v.Data = json.RawMessage(`"wrong-private-canary"`) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := input
			change(&bad)
			if _, err := client.Result(t.Context(), bad); err == nil || errors.Is(err, cpra.ErrAmbiguous) || strings.Contains(err.Error(), "canary") {
				t.Fatal("invalid result accepted or leaked", err)
			}
		})
	}
	after, _, _ := f.store.WorkerExecution(t.Context(), input.ExecutionID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid result changed committed execution")
	}
	input.Status, input.Data = "noData", nil
	receipt, err := client.Result(t.Context(), input)
	if err != nil || receipt.Disposition != api.ReceiptDispositionFinalized {
		t.Fatal("missing result could not finalize under strict result schema", err)
	}
	m, _ := f.store.Get("monitor-one")
	if m.TotalChecks != 0 || !m.LastCheck.IsZero() || m.Generation != before.Intent.Generation {
		t.Fatal("missing result invented a health observation")
	}
}

func TestWorkerResultHTTPAdmissionBoundaries(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, token := range []string{"", managementOperatorToken, managementReaderToken, strings.Repeat("x", 40)} {
		body := &sessionUnreadBody{}
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/result", body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.result(&sessionResponseRecorder{w}, r)
		workerSessionProblem(t, w, 401, "unauthorized")
		if body.reads.Load() != 0 {
			t.Fatal("unauthorized result body read")
		}
	}
	for _, test := range []struct {
		body, code string
		status     int
	}{
		{strings.Repeat("x", maxWorkerResultBodyBytes+1), "workerResultTooLarge", 413},
		{`{"status":"success","status":"failure"}`, "invalidWorkerResult", 400},
		{`{"secret":"private-result-canary"}`, "invalidWorkerResult", 400},
		{`null`, "invalidWorkerResult", 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/result", strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.result(&sessionResponseRecorder{w}, r)
		workerSessionProblem(t, w, test.status, test.code)
		if strings.Contains(w.Body.String(), "canary") {
			t.Fatal("private input echoed")
		}
	}
	for range maxWorkerResultsPerPrincipal {
		if !f.handler.reserveResult(f.uid) {
			t.Fatal("result quota exhausted early")
		}
	}
	body := &sessionUnreadBody{}
	r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/result", body)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.result(&sessionResponseRecorder{w}, r)
	workerSessionProblem(t, w, 429, "workerResultQuota")
	if body.reads.Load() != 0 || w.Header().Get("Retry-After") == "" {
		t.Fatal("quota rejection read body or omitted backoff")
	}
	for range maxWorkerResultsPerPrincipal {
		f.handler.releaseResult(f.uid)
	}
	if f.handler.resulting != 0 || len(f.handler.results) != 0 {
		t.Fatal("result admission leaked")
	}
	w = httptest.NewRecorder()
	managementHeaders(w)
	writeWorkerResultError(w, errors.New("/private/raft.db private-backend-canary"))
	workerSessionProblem(t, w, 503, "unavailable")
	if strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "raft.db") {
		t.Fatal("backend details exposed")
	}
}

func TestWorkerResultHTTPReadDeadlineReleasesAdmission(t *testing.T) {
	f := newWorkerSessionFixture(t)
	returned := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancel()
		f.handler.result(w, r.WithContext(ctx))
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", u.Host, transport.TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST /api/v2/external-workers/result HTTP/1.1\r\nHost: "+u.Host+"\r\nAuthorization: Bearer "+f.token+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	reply, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	if reply.StatusCode != 503 {
		t.Fatal("deadline status", reply.StatusCode)
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stalled body retained admission")
	}
	if f.handler.resulting != 0 || len(f.handler.results) != 0 {
		t.Fatal("result slot retained")
	}
}

type workerResultVerificationWrapper struct {
	secureconfig.KeyWrapper
	cancel context.CancelFunc
	reject bool
	opens  int
	wraps  int
}

func (w *workerResultVerificationWrapper) Unwrap(ctx context.Context, encrypted, aad []byte) ([]byte, error) {
	if bytes.Contains(aad, []byte("worker-outcome-v1")) {
		w.opens++
		if w.cancel != nil {
			w.cancel()
			return nil, ctx.Err()
		}
		if w.reject {
			return nil, errors.New("private-key-provider-canary")
		}
	}
	return w.KeyWrapper.Unwrap(ctx, encrypted, aad)
}

func (w *workerResultVerificationWrapper) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	w.wraps++
	return nil, errors.New("result verification must not reseal retained state")
}

func TestWorkerResultStartupAuthenticationAndCancellation(t *testing.T) {
	for _, cancelVerification := range []bool{false, true} {
		name := "key-unavailable"
		if cancelVerification {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			client := workerHTTPClient(t, f)
			_, input := startWorkerHTTPCheck(t, f, client)
			if _, err := client.Result(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			saved, _, _ := f.store.WorkerExecution(t.Context(), input.ExecutionID)
			before, _ := f.store.Get("monitor-one")
			index := f.store.Status().CommittedIndex
			local, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
			if err != nil {
				t.Fatal(err)
			}
			wrapper := &workerResultVerificationWrapper{KeyWrapper: local, reject: !cancelVerification}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelVerification {
				wrapper.cancel = cancel
			}
			sealer, err := secureconfig.NewSealer(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := management.NewCatalog(f.store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			err = catalog.Verify(ctx)
			if err == nil || wrapper.opens != 1 || wrapper.wraps != 0 || catalog.Ready() || strings.Contains(err.Error(), "canary") {
				t.Fatal("startup admitted an unverifiable result or leaked diagnostics", err)
			}
			if cancelVerification {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation classification", err)
				}
				wrapper.cancel = nil
				if err := catalog.Verify(t.Context()); err != nil || !catalog.Ready() || wrapper.opens != 2 || wrapper.wraps != 0 {
					t.Fatal("cancelled verification could not resume", err)
				}
			} else if f.store.Status().Ready || !errors.Is(err, management.ErrUnavailable) {
				t.Fatal("unverifiable result did not close readiness", err)
			}
			retained, _, readErr := f.store.WorkerExecution(t.Context(), input.ExecutionID)
			after, _ := f.store.Get("monitor-one")
			if cancelVerification && (readErr != nil || !reflect.DeepEqual(saved, retained)) {
				t.Fatal("retryable verification changed the retained outcome", readErr)
			}
			if !cancelVerification && readErr == nil {
				t.Fatal("failed verification left protected reads available")
			}
			if !reflect.DeepEqual(before, after) || f.store.Status().CommittedIndex != index {
				t.Fatal("read-only verification changed committed state")
			}
		})
	}
}
