//go:build externaljobs

package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Public external configuration admission stays closed. This fixture installs
// the authenticated source through the private catalog and owner command path.
func queueWorkerHTTPCheck(t *testing.T, f *workerSessionFixture) persistence.WorkerExecutionRecord {
	t.Helper()
	ctx := t.Context()
	at := time.Now().UTC()
	job, ok, err := f.store.JobType(ctx, "session-check")
	if err != nil || !ok {
		t.Fatal("fixture JobType", err)
	}
	version := job.Versions["v1"]
	spec := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{
		Type: "external", Config: json.RawMessage(`{"jobTypeID":"session-check","version":"v1","parameters":{"large":9007199254740993},"credentialProfile":"worker-local-profile"}`),
	}}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "Monitor", ID: "monitor-one"}, UID: uuid.NewString(), Revision: uuid.NewString(),
		Generation: 1, Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at}
	record.JobTypeReferences = []persistence.JobTypeReference{{JobTypeID: "session-check", JobTypeUID: version.Record.UID,
		Version: "v1", Revision: version.Record.Revision, Category: "check"}}
	resource := api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: record.Key.ID, UID: record.UID, ResourceVersion: record.Revision, Generation: 1}, Spec: raw}
	plain, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	record.Payload, err = sealer.Seal(ctx, record.Binding(f.store.Status().NodeID), plain)
	if err != nil {
		t.Fatal(err)
	}
	results, err := f.store.Submit(ctx, []persistence.Command{{Kind: "catalog", At: at,
		Catalog: &persistence.CatalogMutation{Record: record, Create: true, OperationID: record.Revision, Actor: "operator"}}})
	if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
		t.Fatalf("fixture catalog: %v %+v", err, results)
	}
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: record.Key, UID: record.UID, Revision: record.Revision}}}
	monitor := persistence.Monitor{ID: record.Key.ID, CatalogUID: record.UID, CatalogRevision: record.Revision, Revision: uuid.NewString(),
		Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Healthy: 1, Unhealthy: 1}}
	results, err = f.store.Submit(ctx, []persistence.Command{{Kind: "configure", At: at, MonitorID: monitor.ID, Revision: monitor.Revision, Config: &monitor, Guard: &guard}})
	if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
		t.Fatalf("fixture configure: %v %+v", err, results)
	}
	monitor, ok = f.store.Get(monitor.ID)
	if !ok {
		t.Fatal("fixture monitor absent")
	}
	in := persistence.WorkerExecutionIntent{ID: uuid.NewString(), Revision: uuid.NewString(), MonitorID: monitor.ID, MonitorUID: monitor.CatalogUID,
		MonitorRevision: monitor.Revision, ControlRevision: monitor.ControlRevision, Category: "check", Generation: 1,
		Source: record.Key, SourceUID: record.UID, SourceRevision: record.Revision, JobType: record.JobTypeReferences[0], Guard: guard,
		Scheduled: at, Deadline: at.Add(5 * time.Second)}
	in, err = f.handler.catalog.PrepareWorkerExecution(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := f.store.CommitWorkerExecution(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func workerHTTPClient(t *testing.T, f *workerSessionFixture) *cpra.WorkerClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/external-workers/poll", f.handler.poll)
	mux.HandleFunc("POST /api/v2/external-workers/start", f.handler.start)
	httpServer := httptest.NewTLSServer(mux)
	t.Cleanup(httpServer.Close)
	client, err := cpra.NewWorkerClient(cpra.Config{BaseURL: httpServer.URL, AuthToken: f.token, HTTPClient: httpServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func startWorkerOffer(a api.Assignment, mode api.StartMode) api.StartRequest {
	return api.StartRequest{ServerID: a.ServerID, SessionID: a.SessionID, ExecutionID: a.ExecutionID, ExecutionRevision: a.ExecutionRevision, LeaseID: a.LeaseID, Mode: mode}
}

func TestWorkerStartHTTPRealSDKOfferAndOneGrant(t *testing.T) {
	f := newWorkerSessionFixture(t)
	execution := queueWorkerHTTPCheck(t, f)
	client := workerHTTPClient(t, f)
	offers, err := client.Poll(t.Context(), f.request)
	if err != nil || offers == nil || len(offers.Items) != 1 {
		t.Fatal("poll offer", err)
	}
	a := offers.Items[0]
	if a.ExecutionID != execution.Intent.ID || a.IncarnationUID != execution.Intent.MonitorUID || a.CredentialProfile != "worker-local-profile" || !bytes.Contains(a.Parameters, []byte("9007199254740993")) {
		t.Fatal("original assignment identity or payload changed")
	}
	replay, err := client.Poll(t.Context(), f.request)
	if err != nil || !reflect.DeepEqual(offers, replay) {
		t.Fatal("poll replay", err)
	}
	pending, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeReconcile))
	if err != nil || pending.Disposition != api.StartDispositionPending {
		t.Fatal("reconciliation started offered work", err)
	}
	granted, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
	if err != nil || granted.Disposition != api.StartDispositionGranted || granted.GrantID == "" {
		t.Fatal("initial start", err)
	}
	for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
		repeated, err := client.Start(t.Context(), startWorkerOffer(a, mode))
		if err != nil || repeated.Disposition != api.StartDispositionStarted || repeated.GrantID != granted.GrantID || !repeated.Deadline.Equal(granted.Deadline) {
			t.Fatal("duplicate start granted or changed original grant", err)
		}
	}
	bad := startWorkerOffer(a, api.StartModeBegin)
	bad.LeaseID = uuid.NewString()
	if _, err := client.Start(t.Context(), bad); err == nil {
		t.Fatal("foreign lease accepted")
	}
}

type lostWorkerStartReply struct {
	*persistence.Store
	lost bool
}

func (s *lostWorkerStartReply) CommitWorkerStart(ctx context.Context, a persistence.WorkerAuthority, r persistence.WorkerStartRequest) (persistence.WorkerStartResponse, error) {
	v, err := s.Store.CommitWorkerStart(ctx, a, r)
	if err == nil && !s.lost && r.Mode == "begin" {
		s.lost = true
		return persistence.WorkerStartResponse{}, persistence.ErrCommitUnconfirmed
	}
	return v, err
}

func TestWorkerStartHTTPLostReplyReconcilesOriginalGrant(t *testing.T) {
	f := newWorkerSessionFixture(t)
	queueWorkerHTTPCheck(t, f)
	client := workerHTTPClient(t, f)
	offers, err := client.Poll(t.Context(), f.request)
	if err != nil || len(offers.Items) != 1 {
		t.Fatal("poll", err)
	}
	f.handler.store = &lostWorkerStartReply{Store: f.store}
	if _, err = client.Start(t.Context(), startWorkerOffer(offers.Items[0], api.StartModeBegin)); err == nil {
		t.Fatal("lost reply reported success")
	}
	recovered, err := client.Start(t.Context(), startWorkerOffer(offers.Items[0], api.StartModeReconcile))
	if err != nil || recovered.Disposition != api.StartDispositionStarted || recovered.GrantID == "" {
		t.Fatal("lost reply did not reconcile original start", err)
	}
}

func TestWorkerStartHTTPBoundaries(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, token := range []string{"", managementOperatorToken, managementReaderToken, strings.Repeat("x", 40)} {
		body := &sessionUnreadBody{}
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/start", body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.start(&sessionResponseRecorder{w}, r)
		workerSessionProblem(t, w, 401, "unauthorized")
		if body.reads.Load() != 0 {
			t.Fatal("unauthorized body read")
		}
	}
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{strings.Repeat("x", maxWorkerStartBodyBytes+1), 413, "workerStartTooLarge"},
		{`{"mode":"begin","mode":"reconcile"}`, 400, "invalidWorkerStart"},
		{`{"mode":"begin","secret":"private-start-canary"}`, 400, "invalidWorkerStart"},
		{`null`, 400, "invalidWorkerStart"},
		{`{"mode":"begin"}`, 400, "invalidWorkerStart"},
	} {
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/start", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.start(&sessionResponseRecorder{w}, r)
		workerSessionProblem(t, w, tc.status, tc.code)
		if strings.Contains(w.Body.String(), "private-start-canary") {
			t.Fatal("body echoed")
		}
	}
	for n := 0; n < maxWorkerStartsPerPrincipal; n++ {
		if !f.handler.reserveStart(f.uid) {
			t.Fatal("per-worker quota early")
		}
	}
	if f.handler.reserveStart(f.uid) {
		t.Fatal("per-worker quota exceeded")
	}
	for n := 0; n < maxWorkerStartsPerPrincipal; n++ {
		f.handler.releaseStart(f.uid)
	}
	for n := 0; n < maxConcurrentWorkerStarts; n++ {
		if !f.handler.reserveStart(uuid.NewString()) {
			t.Fatal("global quota early")
		}
	}
	if f.handler.reserveStart("over-limit") {
		t.Fatal("global quota exceeded")
	}
	for uid, count := range f.handler.starts {
		for n := 0; n < count; n++ {
			f.handler.releaseStart(uid)
		}
	}
	if f.handler.starting != 0 || len(f.handler.starts) != 0 {
		t.Fatal("admission leaked")
	}
	w := httptest.NewRecorder()
	managementHeaders(w)
	writeWorkerStartError(w, errors.New("/private/raft.db secret-backend-canary"))
	workerSessionProblem(t, w, 503, "unavailable")
	if strings.Contains(w.Body.String(), "raft.db") || strings.Contains(w.Body.String(), "canary") {
		t.Fatal("backend diagnostic exposed")
	}
	for _, cause := range []error{persistence.ErrWorkerAuthorityDenied, persistence.ErrWorkerExecutionConflict, persistence.ErrWorkerExecutionExpired} {
		w := httptest.NewRecorder()
		managementHeaders(w)
		writeWorkerStartError(w, errors.Join(persistence.ErrCommitUnconfirmed, cause))
		workerSessionProblem(t, w, 503, "workerStartUnconfirmed")
	}
}

func TestWorkerStartHTTPReadDeadlineReleasesAdmission(t *testing.T) {
	f := newWorkerSessionFixture(t)
	returned := make(chan struct{})
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancel()
		f.handler.start(w, r.WithContext(ctx))
	}))
	defer httpServer.Close()
	u, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := httpServer.Client().Transport.(*http.Transport)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", u.Host, transport.TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(conn, "POST /api/v2/external-workers/start HTTP/1.1\r\nHost: "+u.Host+"\r\nAuthorization: Bearer "+f.token+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
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
		t.Fatal("body blocked admission")
	}
	if f.handler.starting != 0 || len(f.handler.starts) != 0 {
		t.Fatal("body retained slot")
	}
}

type workerProjectionWrapper struct {
	secureconfig.KeyWrapper
	hook func()
}

func (w *workerProjectionWrapper) Unwrap(ctx context.Context, encrypted, aad []byte) ([]byte, error) {
	key, err := w.KeyWrapper.Unwrap(ctx, encrypted, aad)
	if err == nil && w.hook != nil && bytes.Contains(aad, []byte("WorkerExecution")) {
		hook := w.hook
		w.hook = nil
		hook()
	}
	return key, err
}

func TestWorkerAssignmentProjectionRechecksControlsAndCancellation(t *testing.T) {
	for _, cancelDuringOpen := range []bool{false, true} {
		t.Run(map[bool]string{false: "controls", true: "cancellation"}[cancelDuringOpen], func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			execution := queueWorkerHTTPCheck(t, f)
			digest := sha256.Sum256([]byte(f.token))
			authority, err := f.store.AuthenticateWorker(t.Context(), hex.EncodeToString(digest[:]), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			response, err := f.store.CommitWorkerPoll(t.Context(), authority, persistence.WorkerPollRequest{ServerID: f.request.ServerID, WorkerID: f.request.WorkerID, ClientSessionID: f.request.ClientSessionID,
				PollSequence: 1, Capacity: 1, Limit: 1, Capabilities: []persistence.WorkerSessionCapability{{JobTypeID: "session-check", Version: "v1", Category: "check"}}})
			if err != nil || len(response.Offers) != 1 {
				t.Fatal("offer fixture", err)
			}
			local, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
			if err != nil {
				t.Fatal(err)
			}
			wrapper := &workerProjectionWrapper{KeyWrapper: local}
			sealer, err := secureconfig.NewSealer(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := management.NewCatalog(f.store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			if err = catalog.Verify(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			wrapper.hook = func() {
				called = true
				if cancelDuringOpen {
					cancel()
					return
				}
				in := execution.Intent
				at := time.Now().UTC()
				control := persistence.ControlCommand{Action: "snooze", MonitorUID: in.MonitorUID, ExpectedRevision: in.ControlRevision, Revision: uuid.NewString(), OperationID: uuid.NewString(), Actor: "operator", Reason: "projection race", Until: at.Add(time.Minute)}
				control.OperationID = control.Revision
				results, err := f.store.Submit(t.Context(), []persistence.Command{{Kind: "control", MonitorID: in.MonitorID, At: at, Control: &control}})
				if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
					t.Fatalf("control during projection: %v %+v", err, results)
				}
			}
			page, err := catalog.WorkerAssignments(ctx, authority, response)
			if !called || err == nil || !reflect.DeepEqual(page, api.Assignments{}) {
				t.Fatal("interrupted projection exposed a partial assignment", err)
			}
			if cancelDuringOpen && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation classification", err)
			}
			if !catalog.Ready() {
				t.Fatal("ordinary race poisoned catalog")
			}
			if strings.Contains(err.Error(), "worker-local-profile") || strings.Contains(err.Error(), "9007199254740993") {
				t.Fatal("assignment data leaked in error")
			}
		})
	}
}

func TestWorkerProtocolReadinessHonorsRequestCancellation(t *testing.T) {
	f := newWorkerSessionFixture(t)
	f.handler.ready = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	for _, operation := range []string{"poll", "start"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			body := &sessionUnreadBody{}
			r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/"+operation, body).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+f.token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "poll" {
					f.handler.poll(&sessionResponseRecorder{w}, r)
				} else {
					f.handler.start(&sessionResponseRecorder{w}, r)
				}
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("readiness ignored request cancellation")
			}
			workerSessionProblem(t, w, 503, "requestInterrupted")
			if body.reads.Load() != 0 || f.handler.starting != 0 || len(f.handler.polls) != 0 {
				t.Fatal("readiness failure read body or retained admission")
			}
		})
	}
}
