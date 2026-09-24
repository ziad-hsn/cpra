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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type workerSessionFixture struct {
	handler *workerSessionHTTP
	store   *persistence.Store
	dir     string
	token   string
	request api.PollRequest
	uid     string
}

type sessionResponseRecorder struct{ *httptest.ResponseRecorder }

func (*sessionResponseRecorder) SetReadDeadline(time.Time) error { return nil }

func newWorkerSessionFixture(t *testing.T) *workerSessionFixture {
	t.Helper()
	f, store, directory := newJobTypeHTTPFixture(t)
	job, err := f.sdk.JobTypes().Create(t.Context(), httpJobType("session-check"))
	if err != nil {
		t.Fatal(err)
	}
	f.http.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	grantsPath, tokenPath := filepath.Join(private, "grants.json"), filepath.Join(private, "worker.token")
	grants, err := json.Marshal(struct {
		Grants []persistence.WorkerGrant `json:"grants"`
	}{
		Grants: []persistence.WorkerGrant{{JobTypeID: "session-check", JobTypeUID: job.Data.Metadata.UID, Version: "v1", Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"monitor-one"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grantsPath, grants, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{
		Action: "issue", DataDirectory: directory, Actor: "operator", WorkerID: "worker-one", GrantsFile: grantsPath, TokenOutput: tokenPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	store, err = persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler := &workerSessionHTTP{auth: f.server.cfg.ManagementAuth, store: store,
		catalog: catalog,
		ready:   store.ControllerHealthContext,
		admit: func(ctx context.Context, fn func() error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fn()
		},
	}
	return &workerSessionFixture{handler: handler, store: store, dir: directory, token: strings.TrimSpace(string(token)), uid: report.Workers[0].UID,
		request: api.PollRequest{WorkerID: "worker-one", ServerID: report.ProtocolServerID, ClientSessionID: uuid.NewString(), PollSequence: 1,
			Capacity: 1, Limit: 1, Capabilities: []api.WorkerCapability{{JobTypeID: "session-check", Version: "v1", Kind: "check"}},
		},
	}
}

func (f *workerSessionFixture) call(t *testing.T, request api.PollRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/poll", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.poll(&sessionResponseRecorder{w}, r)
	return w
}

func workerSessionProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var problem api.Problem
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.Code != code || problem.RequestID == "" {
		t.Fatalf("worker response: status=%d code=%s; want %d %s", response.Code, problem.Code, status, code)
	}
}

func TestWorkerSessionHTTPRaftReplayAndSDK(t *testing.T) {
	f := newWorkerSessionFixture(t)
	// This mux qualifies only the session adapter; startup exposes no worker routes.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/external-workers/poll", f.handler.poll)
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	client, err := cpra.NewWorkerClient(cpra.Config{BaseURL: server.URL, AuthToken: f.token, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Poll(t.Context(), f.request)
	if err != nil || first == nil || first.WorkerUID != f.uid || first.SessionID == "" || first.Items == nil || len(first.Items) != 0 {
		t.Fatal("session commit failed", err)
	}
	replay, err := client.Poll(t.Context(), f.request)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatal("initial replay changed committed identity or deadline", err)
	}
	changed := f.request
	changed.Capacity = 2
	workerSessionProblem(t, f.call(t, changed), 409, "workerSessionConflict")
	changed = f.request
	changed.ClientSessionID = uuid.NewString()
	workerSessionProblem(t, f.call(t, changed), 409, "workerSessionConflict")
	changed = f.request
	changed.ServerID = "different-server"
	workerSessionProblem(t, f.call(t, changed), 409, "workerServerIdentity")
	changed = f.request
	changed.WorkerID = "another-worker"
	workerSessionProblem(t, f.call(t, changed), 400, "invalidWorkerPoll")
	f.request.SessionID, f.request.PollSequence = first.SessionID, 2
	second, err := client.Poll(t.Context(), f.request)
	if err != nil || second.SessionID != first.SessionID || second.PollSequence != 2 || second.WorkerUID != f.uid {
		t.Fatal("next poll changed session identity", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = f.dir
	reopened, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.handler.store = reopened
	f.handler.catalog = nil // Empty-session replay remains independent of projection.
	f.handler.ready = reopened.ControllerHealthContext
	_, err = client.Poll(t.Context(), f.request)
	if !errors.Is(err, cpra.ErrWorkerSessionExpired) {
		t.Fatal("owner restart did not reject original session", err)
	}
	f.request.SessionID, f.request.PollSequence, f.request.ClientSessionID = "", 1, uuid.NewString()
	resumed, err := client.Poll(t.Context(), f.request)
	if err != nil || resumed.SessionID == first.SessionID || resumed.ServerID != first.ServerID || resumed.WorkerUID != f.uid {
		t.Fatal("restart lost server or provisioned worker identity", err)
	}
}

type sessionUnreadBody struct{ reads atomic.Int32 }

func (b *sessionUnreadBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, errors.New("private-body-canary")
}
func (*sessionUnreadBody) Close() error { return nil }

func TestWorkerSessionHTTPAdmissionBoundaries(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, token := range []string{"", managementOperatorToken, managementReaderToken, strings.Repeat("x", 40)} {
		body := &sessionUnreadBody{}
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/poll", body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.poll(&sessionResponseRecorder{w}, r)
		workerSessionProblem(t, w, 401, "unauthorized")
		if body.reads.Load() != 0 {
			t.Fatal("unauthorized body was read")
		}
	}
	for _, mutate := range []func(*api.PollRequest){
		func(r *api.PollRequest) { r.Capacity = 1 << 40 },
		func(r *api.PollRequest) { r.Limit = 2 },
		func(r *api.PollRequest) { r.WaitSeconds = 26 },
		func(r *api.PollRequest) { r.Capabilities = make([]api.WorkerCapability, 65) },
	} {
		r := f.request
		mutate(&r)
		workerSessionProblem(t, f.call(t, r), 400, "invalidWorkerPoll")
	}
	for _, field := range []string{"sessionID", "waitSeconds"} {
		for _, null := range []bool{false, true} {
			raw, _ := json.Marshal(f.request)
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			delete(object, field)
			if null {
				object[field] = json.RawMessage(`null`)
			}
			raw, _ = json.Marshal(object)
			r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/poll", bytes.NewReader(raw))
			r.Header.Set("Authorization", "Bearer "+f.token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.handler.poll(&sessionResponseRecorder{w}, r)
			workerSessionProblem(t, w, 400, "invalidWorkerPoll")
		}
	}
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"oversize", strings.Repeat("x", maxWorkerPollBodyBytes+1), 413, "workerPollTooLarge"},
		{"duplicate", `{"workerID":"worker-one","workerID":"other"}`, 400, "invalidWorkerPoll"},
		{"unknown", `{"secret":"private-body-canary"}`, 400, "invalidWorkerPoll"},
		{"null", `null`, 400, "invalidWorkerPoll"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/poll", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+f.token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.handler.poll(&sessionResponseRecorder{w}, r)
			workerSessionProblem(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "private-body-canary") {
				t.Fatal("request echoed")
			}
		})
	}
	f.handler.ready = func(context.Context) error { return persistence.ErrWorkerSessionUnavailable }
	workerSessionProblem(t, f.call(t, f.request), 503, "unavailable")
	f.handler.ready = func(context.Context) error { return nil }
	f.handler.admit = func(context.Context, func() error) error { return errors.New("/private/state/raft.db backend-canary") }
	w := f.call(t, f.request)
	workerSessionProblem(t, w, 503, "unavailable")
	if strings.Contains(w.Body.String(), "raft.db") || strings.Contains(w.Body.String(), "canary") {
		t.Fatal("backend error exposed")
	}
}

func TestWorkerSessionHTTPReadDeadlineReleasesAdmission(t *testing.T) {
	f := newWorkerSessionFixture(t)
	returned := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancel()
		f.handler.poll(w, r.WithContext(ctx))
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
	if _, err := io.WriteString(conn, "POST /api/v2/external-workers/poll HTTP/1.1\r\nHost: "+u.Host+"\r\nAuthorization: Bearer "+f.token+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal("slow-body response", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("slow-body status", response.StatusCode)
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("slow body retained admission")
	}
	if !f.handler.reserve(f.uid) {
		t.Fatal("expired read did not release worker slot")
	}
	f.handler.release(f.uid)
}

type sessionBlockingStore struct {
	*persistence.Store
	entered, release chan struct{}
}

func (s *sessionBlockingStore) CommitWorkerPoll(ctx context.Context, a persistence.WorkerAuthority, r persistence.WorkerPollRequest) (persistence.WorkerPollResponse, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return persistence.WorkerPollResponse{}, ctx.Err()
	}
	return s.Store.CommitWorkerPoll(ctx, a, r)
}

func TestWorkerSessionHTTPConcurrentPollAndCancellation(t *testing.T) {
	f := newWorkerSessionFixture(t)
	blocking := &sessionBlockingStore{Store: f.store, entered: make(chan struct{}), release: make(chan struct{})}
	f.handler.store = blocking
	raw, _ := json.Marshal(f.request)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/poll", bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.handler.poll(&sessionResponseRecorder{first}, r) }()
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not enter store")
	}
	workerSessionProblem(t, f.call(t, f.request), 429, "workerSessionQuota")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not cancel")
	}
	workerSessionProblem(t, first, 503, "requestInterrupted")
	f.handler.store = f.store
	if w := f.call(t, f.request); w.Code != 200 {
		t.Fatal("canceled poll retained reservation", w.Code)
	}
	if !f.handler.reserve("slot-one") || f.handler.reserve("slot-one") {
		t.Fatal("duplicate reservation")
	}
	f.handler.release("slot-one")
	for i := 0; i < maxConcurrentWorkerPolls; i++ {
		if !f.handler.reserve(uuid.NewString()) {
			t.Fatal("quota filled early")
		}
	}
	if f.handler.reserve("overflow") {
		t.Fatal("global poll quota exceeded")
	}
}

var _ io.ReadCloser = (*sessionUnreadBody)(nil)
