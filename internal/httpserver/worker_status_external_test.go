//go:build externaljobs

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/localadmin"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func reopenWorkerStatusFixture(t *testing.T, f *workerSessionFixture) {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = f.dir
	store, err := persistence.Open(t.Context(), cfg)
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
	if err := catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.store, f.handler.store, f.handler.catalog = store, store, catalog
	f.handler.ready = store.ControllerHealthContext
}

func provisionStatusWorker(t *testing.T, f *workerSessionFixture, id string) {
	t.Helper()
	job, ok, err := f.store.JobType(t.Context(), "session-check")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(struct {
		Grants []persistence.WorkerGrant `json:"grants"`
	}{
		Grants: []persistence.WorkerGrant{{JobTypeID: "session-check", JobTypeUID: job.Current.Record.UID, Version: "v1", Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"monitor-one"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	grantsFile := filepath.Join(dir, "grants.json")
	if err := os.WriteFile(grantsFile, grants, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = localadmin.AdministerWorkerAuthentication(t.Context(), localadmin.WorkerAuthenticationRequest{Action: "issue", DataDirectory: f.dir, Actor: "operator", WorkerID: id, GrantsFile: grantsFile, TokenOutput: filepath.Join(dir, "worker.token")})
	if err != nil {
		t.Fatal(err)
	}
	reopenWorkerStatusFixture(t, f)
}

func workerStatusClient(t *testing.T, s *Server, transport *http.Client, token string) *cpra.Client {
	t.Helper()
	client, err := cpra.New(cpra.Config{BaseURL: "https://" + s.srv.Addr, AuthToken: token, HTTPClient: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestWorkerStatusProductionPaginationAndAuthority(t *testing.T) {
	f := newWorkerSessionFixture(t)
	provisionStatusWorker(t, f, "worker-aaa")
	s, worker, transport := startWorkerProductionServer(t, f, true, nil)
	reader := workerStatusClient(t, s, transport, managementReaderToken)
	before := f.store.Status().CommittedIndex
	first, err := reader.Workers(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(first.Data.Items) != 1 || first.Data.NextCursor == "" || first.Data.Items[0].WorkerID != "worker-aaa" || first.Data.GeneratedAt.IsZero() {
		t.Fatal("first observation page", first, err)
	}
	if first.Data.Items[0].LastSeenAt != nil || first.Data.Items[0].CapacityAvailable || f.store.Status().CommittedIndex != before {
		t.Fatal("observation fabricated a poll or changed durable state")
	}
	if _, err := worker.Poll(t.Context(), f.request); err != nil {
		t.Fatal(err)
	}
	page, err := reader.Workers(t.Context(), cpra.ListOptions{Cursor: first.Data.NextCursor, Limit: 1})
	if err != nil || len(page.Data.Items) != 1 || page.Data.Items[0].WorkerID != "worker-one" || page.Data.NextCursor != "" || !page.Data.GeneratedAt.Equal(first.Data.GeneratedAt) {
		t.Fatal("snapshot continuation", page, err)
	}
	if page.Data.Items[0].LastSeenAt != nil || page.Data.Items[0].CapacityAvailable {
		t.Fatal("continuation included a poll committed after snapshot capture")
	}
	repeated, err := reader.Workers(t.Context(), cpra.ListOptions{Cursor: first.Data.NextCursor, Limit: 1})
	if err != nil || !reflect.DeepEqual(page.Data, repeated.Data) {
		t.Fatal("page replay changed", err)
	}
	fresh, err := reader.Workers(t.Context(), cpra.ListOptions{})
	if err != nil || len(fresh.Data.Items) != 2 || !fresh.Data.Items[1].CapacityAvailable || fresh.Data.Items[1].SessionStatus != "current" || fresh.Data.Items[1].LastSeenAt == nil {
		t.Fatal("current poll missing", err)
	}
	operator := workerStatusClient(t, s, transport, managementOperatorToken)
	if _, err := operator.Workers(t.Context(), cpra.ListOptions{Cursor: first.Data.NextCursor}); err == nil {
		t.Fatal("cursor crossed operator identity")
	}
	for _, token := range []string{f.token, managementLegacyToken} {
		client := workerStatusClient(t, s, transport, token)
		if _, err := client.Workers(t.Context(), cpra.ListOptions{}); err == nil {
			t.Fatal("non-management identity read workers")
		}
	}
	for _, opts := range []cpra.ListOptions{{Selector: "team=ops"}, {MonitorID: "monitor-one"}, {Cursor: first.Data.NextCursor, Limit: 2}} {
		if _, err := reader.Workers(t.Context(), opts); err == nil {
			t.Fatal("unsupported or conflicting query accepted")
		}
	}
	if err := s.StopAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Workers(t.Context(), cpra.ListOptions{}); err != nil {
		t.Fatal("drain lost read-only diagnostics", err)
	}
}

func TestWorkerStatusProductionUnknownAndSnapshotRestart(t *testing.T) {
	f := newWorkerSessionFixtureForCategory(t, "notification")
	queueWorkerHTTPAction(t, f, "notification")
	s, worker, transport := startWorkerProductionServer(t, f, true, nil)
	reader := workerStatusClient(t, s, transport, managementReaderToken)
	page, err := worker.Poll(t.Context(), f.request)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("offer", err)
	}
	assignment := page.Items[0]
	start, err := worker.Start(t.Context(), startWorkerOffer(assignment, api.StartModeBegin))
	if err != nil {
		t.Fatal(err)
	}
	active, err := reader.Workers(t.Context(), cpra.ListOptions{})
	if err != nil || active.Data.Items[0].Active != 1 || active.Data.Items[0].UnknownActions != 0 {
		t.Fatal("grant count", err)
	}
	outcome := api.Outcome{ServerID: assignment.ServerID, WorkerUID: assignment.WorkerUID, ExecutionID: assignment.ExecutionID, GrantID: start.GrantID, Kind: "notification", Status: "unknown", Diagnostic: "private-status-diagnostic", Evidence: []string{"private-status-evidence"}}
	if _, err := worker.Result(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}
	unknown, err := reader.Workers(t.Context(), cpra.ListOptions{})
	if err != nil || unknown.Data.Items[0].Active != 0 || unknown.Data.Items[0].UnknownActions != 1 {
		t.Fatal("unknown action count", err)
	}
	raw, _ := json.Marshal(unknown.Data)
	for _, private := range []string{f.token, "private-status-diagnostic", "private-status-evidence", assignment.ExecutionID, start.GrantID} {
		if strings.Contains(string(raw), private) {
			t.Fatal("worker observation leaked private execution or credentials")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.StopContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenWorkerStatusFixture(t, f)
	s, _, transport = startWorkerProductionServer(t, f, true, nil)
	reader = workerStatusClient(t, s, transport, managementReaderToken)
	restored, err := reader.Workers(t.Context(), cpra.ListOptions{})
	if err != nil || restored.Data.Items[0].UnknownActions != 1 || restored.Data.Items[0].WorkerUID != unknown.Data.Items[0].WorkerUID || restored.Data.Items[0].SessionStatus != "previous_owner" || restored.Data.Items[0].CapacityAvailable {
		t.Fatal("restart invented capacity or lost held action", restored, err)
	}
}

func TestWorkerStatusProductionOptInAndStorageErrors(t *testing.T) {
	f := newWorkerSessionFixture(t)
	s, _, transport := startWorkerProductionServer(t, f, false, nil)
	reader := workerStatusClient(t, s, transport, managementReaderToken)
	if _, err := reader.Workers(t.Context(), cpra.ListOptions{}); err == nil {
		t.Fatal("disabled extension exposed workers")
	}
	s, _, transport = startWorkerProductionServer(t, f, true, nil)
	f.store.MarkUnavailable(errors.New("private-status-path /var/lib/private-rafter"))
	r, err := http.NewRequestWithContext(t.Context(), "GET", "https://"+s.srv.Addr+"/api/v2/external-workers", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+managementReaderToken)
	response, err := transport.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil || response.StatusCode != 503 || bytes.Contains(raw, []byte("private-status-path")) || bytes.Contains(raw, []byte("private-rafter")) {
		t.Fatal("storage failure not sanitized", response.StatusCode, err)
	}
}

func TestWorkerStatusSnapshotQuotaAndActiveReaderLifetime(t *testing.T) {
	m, clock := jobTypeSnapshotHTTP(t)
	var reads []*snapshotRead
	t.Cleanup(func() {
		for _, read := range reads {
			m.releaseSnapshotRead(read)
		}
	})
	for _, principal := range []string{"one", "one", "two", "two", "three", "three", "four", "four"} {
		read, err := m.beginSnapshotRead(t.Context(), "ExternalWorker", url.Values{}, principal, 1)
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, read)
	}
	for _, principal := range []string{"one", "five"} {
		_, err := m.beginSnapshotRead(t.Context(), "ExternalWorker", url.Values{}, principal, 1)
		assertJobTypeSnapshotFailure(t, err, 429, "snapshotQuota")
	}
	clock.Add(int64(2 * managementCursorTTL))
	m.mu.Lock()
	m.pruneSnapshots(m.now(), "", 0)
	m.unlockSnapshots()
	if len(m.snapshots) != 0 || m.workerSnapshotCount.Load() != maxWorkerSnapshots {
		t.Fatal("expiry released in-flight reader reservations")
	}
	for _, read := range reads {
		m.releaseSnapshotRead(read)
	}
	reads = nil
	if m.workerSnapshotCount.Load() != 0 {
		t.Fatal("released readers retained capture quota")
	}
}
