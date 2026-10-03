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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func queueWorkerHTTPAction(t *testing.T, f *workerSessionFixture, category string) persistence.WorkerExecutionRecord {
	t.Helper()
	return queueWorkerHTTPActionSource(t, f, category, false)
}

func queueWorkerHTTPActionSource(t *testing.T, f *workerSessionFixture, category string, inline bool) persistence.WorkerExecutionRecord {
	t.Helper()
	ctx, at := t.Context(), time.Now().UTC()
	job, ok, err := f.store.JobType(ctx, "session-check")
	if err != nil || !ok {
		t.Fatal(err)
	}
	v := job.Versions["v1"]
	ref := persistence.JobTypeReference{JobTypeID: "session-check", JobTypeUID: v.Record.UID, Version: "v1", Revision: v.Record.Revision, Category: category}
	driver := api.DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"session-check","version":"v1","parameters":{"large":9007199254740993},"credentialProfile":"worker-local-profile"}`)}
	spec := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.invalid"}`)}}}
	var endpoint persistence.CatalogRecord
	if category == "recovery" {
		spec.Recovery = &api.RecoverySpec{Driver: driver}
	} else if inline {
		rules := map[string]api.AlertRule{"red": {Driver: &driver}}
		spec.Notifications = &rules
	} else {
		endpoint = commitWorkerHTTPResource(t, f, persistence.CatalogKey{Kind: "NotificationEndpoint", ID: "endpoint-one"}, driver, nil, []persistence.JobTypeReference{ref})
		ids := []string{endpoint.Key.ID}
		rules := map[string]api.AlertRule{"red": {EndpointRefs: &ids}}
		spec.Notifications = &rules
	}
	var refs []persistence.CatalogKey
	types := []persistence.JobTypeReference{ref}
	if category == "notification" && !inline {
		refs = []persistence.CatalogKey{endpoint.Key}
		types = nil
	}
	root := commitWorkerHTTPResource(t, f, persistence.CatalogKey{Kind: "Monitor", ID: "monitor-one"}, spec, refs, types)
	source := root
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: root.Key, UID: root.UID, Revision: root.Revision}}}
	monitor := persistence.Monitor{ID: root.Key.ID, Revision: uuid.NewString(), Policy: persistence.Policy{
		Enabled: true, Interval: time.Minute, Healthy: 1, Unhealthy: 1, Intervention: category == "recovery", RecoveryBypass: true}}
	if category == "notification" {
		monitor.Policy.Endpoints = map[string]int{"red": 1}
		pin := persistence.WorkerNotificationSource{Kind: "Monitor", ID: root.Key.ID, UID: root.UID, Revision: root.Revision}
		if !inline {
			source = endpoint
			guard.Conditions = append(guard.Conditions, persistence.CatalogCondition{Key: endpoint.Key, UID: endpoint.UID, Revision: endpoint.Revision})
			pin = persistence.WorkerNotificationSource{ID: endpoint.Key.ID, UID: endpoint.UID, Revision: endpoint.Revision}
		}
		monitor.Policy.WorkerNotificationSources = map[string][]persistence.WorkerNotificationSource{"red": {pin}}
	}
	for _, command := range []persistence.Command{
		{Kind: "configure", MonitorID: monitor.ID, Revision: monitor.Revision, At: at, Config: &monitor, Guard: &guard},
		{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, At: at, Generation: 1, Outcome: "failure", Guard: &guard},
	} {
		results, err := f.store.Submit(ctx, []persistence.Command{command})
		if err != nil || len(results) != 1 || results[0].Err != nil {
			t.Fatalf("action setup: %v %+v", err, results)
		}
	}
	monitor, _ = f.store.Get(monitor.ID)
	in := persistence.WorkerExecutionIntent{ID: uuid.NewString(), Revision: uuid.NewString(), MonitorID: monitor.ID, MonitorUID: monitor.CatalogUID,
		MonitorRevision: monitor.Revision, ControlRevision: monitor.ControlRevision, Category: category, Source: source.Key, SourceUID: source.UID,
		SourceRevision: source.Revision, JobType: ref, Guard: guard, Scheduled: at, Deadline: at.Add(5 * time.Second)}
	for _, action := range monitor.Actions {
		if category == "recovery" && action.Kind == "intervention" || category == "notification" && action.Kind == "code" {
			in.ActionID = action.ID
			in.Scheduled = action.NotBefore
			break
		}
	}
	if in.ActionID == "" {
		t.Fatal("fixture did not open an action")
	}
	in, err = f.handler.catalog.PrepareWorkerExecution(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.store.CommitWorkerExecution(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func commitWorkerHTTPResource(t *testing.T, f *workerSessionFixture, key persistence.CatalogKey, spec any, refs []persistence.CatalogKey, types []persistence.JobTypeReference) persistence.CatalogRecord {
	t.Helper()
	at := time.Now().UTC()
	record := persistence.CatalogRecord{Key: key, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at, References: refs}
	record.JobTypeReferences = types
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	resource := api.Resource{APIVersion: api.APIVersion, Kind: key.Kind, Metadata: api.Metadata{ID: key.ID, UID: record.UID, ResourceVersion: record.Revision, Generation: 1}, Spec: raw}
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
	record.Payload, err = sealer.Seal(t.Context(), record.Binding(f.store.Status().NodeID), plain)
	if err != nil {
		t.Fatal(err)
	}
	var conditions []persistence.CatalogCondition
	view, err := f.store.CatalogSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range refs {
		dependency, ok := view.Get(key)
		if !ok {
			t.Fatal("missing fixture dependency")
		}
		conditions = append(conditions, persistence.CatalogCondition{Key: key, UID: dependency.UID, Revision: dependency.Revision})
	}
	results, err := f.store.Submit(t.Context(), []persistence.Command{{Kind: "catalog", At: at, Catalog: &persistence.CatalogMutation{Record: record, Create: true, OperationID: record.Revision, Actor: "operator", Conditions: conditions}}})
	if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
		t.Fatalf("resource setup: %v %+v", err, results)
	}
	return record
}

func TestWorkerLifecycleSDKHeartbeatEvidenceAndRestart(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			f := newWorkerSessionFixtureForCategory(t, category)
			if category == "check" {
				queueWorkerHTTPCheck(t, f)
			} else {
				queueWorkerHTTPAction(t, f, category)
			}
			client := workerHTTPClient(t, f)
			assignments, err := client.Poll(t.Context(), f.request)
			if err != nil || assignments == nil || len(assignments.Items) != 1 {
				t.Fatal("offer", err)
			}
			a := assignments.Items[0]
			start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("start", err)
			}
			heartbeat := api.HeartbeatRequest{ServerID: a.ServerID, SessionID: a.SessionID, WorkerID: f.request.WorkerID, ExecutionID: a.ExecutionID, GrantID: start.GrantID}
			h, err := client.Heartbeat(t.Context(), heartbeat)
			if err != nil || !h.Accepted || h.WorkerUID != f.uid {
				t.Fatal("heartbeat", err)
			}
			status := "unknown"
			if category == "check" {
				status = "noData"
			}
			outcome := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: start.GrantID, Kind: category, Status: status, Evidence: []string{}}
			original, err := client.Result(t.Context(), outcome)
			if err != nil {
				t.Fatal("original outcome", err)
			}
			before, _, _ := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			monitor, _ := f.store.Get(a.MonitorID)
			input := api.LateEvidenceRequest{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, EvidenceID: uuid.NewString(), OriginalReceiptID: original.ReceiptID,
				Diagnostic: "private-late-evidence-canary", Evidence: []string{"private-evidence-reference-canary"}, Data: json.RawMessage(`"private-schema-canary"`)}
			first, err := client.LateEvidence(t.Context(), input)
			if err != nil || first.Disposition != api.ReceiptDispositionAccepted || first.ReceiptID == original.ReceiptID {
				t.Fatal("evidence", first, err)
			}
			replayed, err := client.LateEvidence(t.Context(), input)
			if err != nil || !reflect.DeepEqual(first, replayed) {
				t.Fatal("lost receipt replay", err)
			}
			changed := input
			changed.Diagnostic = "contradictory-private-canary"
			if _, err := client.LateEvidence(t.Context(), changed); !errors.Is(err, cpra.ErrConflict) {
				t.Fatal("conflicting evidence overwritten", err)
			}
			after, _, _ := f.store.WorkerExecution(t.Context(), a.ExecutionID)
			current, _ := f.store.Get(a.MonitorID)
			if len(after.Evidence) != 1 || !reflect.DeepEqual(before.Result, after.Result) || !reflect.DeepEqual(monitor, current) {
				t.Fatal("late evidence changed health or original action")
			}
			raw, _ := json.Marshal(after)
			if bytes.Contains(raw, []byte("canary")) {
				t.Fatal("plaintext retained in execution metadata")
			}
			if err := f.handler.catalog.Verify(t.Context()); err != nil {
				t.Fatal("retained evidence authentication", err)
			}
			if err := f.store.Snapshot(); err != nil {
				t.Fatal(err)
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
			wrapper, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
			sealer, _ := secureconfig.NewSealer(wrapper)
			catalog, err := management.NewCatalog(store, sealer)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.Verify(t.Context()); err != nil {
				t.Fatal("restart verification", err)
			}
			f.handler.store, f.handler.catalog, f.handler.ready = store, catalog, store.ControllerHealthContext
			replayed, err = client.LateEvidence(t.Context(), input)
			if err != nil || !reflect.DeepEqual(first, replayed) {
				t.Fatal("restart changed receipt", err)
			}
			h, err = client.Heartbeat(t.Context(), heartbeat)
			if err != nil || h.Accepted {
				t.Fatal("terminal grant continued after restart", err)
			}
			history, err := store.History().Page(a.MonitorID, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range history.Events {
				if event.Type == "worker_late_evidence" {
					count++
				}
			}
			want := 1
			if category == "check" {
				want = 0
			}
			if count != want {
				t.Fatal("late evidence history lost or duplicated", count)
			}
		})
	}
}

func TestWorkerLifecycleIngressRejectsBeforeReadingUnauthorizedBodies(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, v := range []struct {
		path, code                   string
		handler                      http.HandlerFunc
		limit, maximum, perPrincipal int
		admission                    *workerIngressLimit
	}{
		{"heartbeat", "workerHeartbeat", f.handler.heartbeat, 8 << 10, 64, 16, &f.handler.heartbeats},
		{"late-evidence", "workerEvidence", f.handler.evidence, 128 << 10, 16, 4, &f.handler.evidenceRequests},
	} {
		t.Run(v.path, func(t *testing.T) {
			body := &sessionUnreadBody{}
			r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/"+v.path, body)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			v.handler(&sessionResponseRecorder{w}, r)
			workerSessionProblem(t, w, 401, "unauthorized")
			if body.reads.Load() != 0 {
				t.Fatal("unauthorized body read")
			}
			for _, tc := range []struct {
				raw    string
				status int
				code   string
			}{
				{strings.Repeat("x", v.limit+1), 413, v.code + "TooLarge"},
				{`{"secret":"private-canary"}`, 400, v.code + "Invalid"},
				{`null`, 400, v.code + "Invalid"},
			} {
				r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/"+v.path, strings.NewReader(tc.raw))
				r.Header.Set("Authorization", "Bearer "+f.token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				v.handler(&sessionResponseRecorder{w}, r)
				workerSessionProblem(t, w, tc.status, tc.code)
				if strings.Contains(w.Body.String(), "canary") {
					t.Fatal("private input echoed")
				}
			}
			for n := 0; n < v.perPrincipal; n++ {
				if !v.admission.reserve(f.uid, v.maximum, v.perPrincipal) {
					t.Fatal("early admission rejection")
				}
			}
			body = &sessionUnreadBody{}
			r = httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/"+v.path, body)
			r.Header.Set("Authorization", "Bearer "+f.token)
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			v.handler(&sessionResponseRecorder{w}, r)
			workerSessionProblem(t, w, 429, v.code+"Quota")
			if body.reads.Load() != 0 || w.Header().Get("Retry-After") == "" {
				t.Fatal("quota consumed body or omitted backoff")
			}
			for n := 0; n < v.perPrincipal; n++ {
				v.admission.release(f.uid)
			}
			w = httptest.NewRecorder()
			writeWorkerLifecycleError(w, errors.New("/private/raft.db secret-canary"), v.code)
			if strings.Contains(w.Body.String(), "raft.db") || strings.Contains(w.Body.String(), "canary") {
				t.Fatal("backend details leaked")
			}
		})
	}
}

func TestWorkerLifecycleReadinessCancellationReleasesAdmission(t *testing.T) {
	f := newWorkerSessionFixture(t)
	f.handler.ready = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	for _, v := range []struct {
		path    string
		handler http.HandlerFunc
	}{{"heartbeat", f.handler.heartbeat}, {"late-evidence", f.handler.evidence}} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		body := &sessionUnreadBody{}
		r := httptest.NewRequest(http.MethodPost, "https://cpra.example/api/v2/external-workers/"+v.path, body).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		v.handler(&sessionResponseRecorder{w}, r)
		if w.Code != 503 || body.reads.Load() != 0 {
			t.Fatal("canceled readiness consumed request", w.Code)
		}
	}
	if f.handler.heartbeats.active != 0 || f.handler.evidenceRequests.active != 0 {
		t.Fatal("admission leaked")
	}
}

func TestWorkerLifecycleStalledBodiesReleaseAdmission(t *testing.T) {
	f := newWorkerSessionFixture(t)
	for _, v := range []struct {
		path    string
		handler http.HandlerFunc
		limit   *workerIngressLimit
	}{
		{"heartbeat", f.handler.heartbeat, &f.handler.heartbeats},
		{"late-evidence", f.handler.evidence, &f.handler.evidenceRequests},
	} {
		t.Run(v.path, func(t *testing.T) {
			returned := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(returned)
				ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
				defer cancel()
				v.handler(w, r.WithContext(ctx))
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
			if _, err := io.WriteString(conn, "POST /api/v2/external-workers/"+v.path+" HTTP/1.1\r\nHost: "+u.Host+"\r\nAuthorization: Bearer "+f.token+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 503 {
				t.Fatal("stalled request status", response.StatusCode)
			}
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("stalled body held admission")
			}
			v.limit.mu.Lock()
			active, principals := v.limit.active, len(v.limit.principals)
			v.limit.mu.Unlock()
			if active != 0 || principals != 0 {
				t.Fatal("stalled request retained a slot")
			}
		})
	}
}
