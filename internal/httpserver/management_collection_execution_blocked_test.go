package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type executionBlockedView struct {
	collectionExecutionReadView
	entered chan struct{}
	release chan struct{}
	exited  chan struct{}
}

func (v *executionBlockedView) Page(ctx context.Context, after uint64, limit int, at time.Time) (persistence.CollectionExecutionHistoryPage, error) {
	select {
	case v.entered <- struct{}{}:
	case <-ctx.Done():
		return persistence.CollectionExecutionHistoryPage{}, ctx.Err()
	}
	defer func() { v.exited <- struct{}{} }()
	select {
	case <-v.release:
		return v.collectionExecutionReadView.Page(ctx, after, limit, at)
	case <-ctx.Done():
		return persistence.CollectionExecutionHistoryPage{}, ctx.Err()
	}
}

func executionGateWait(t *testing.T, gate *executionBlockedView) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("TLS request did not enter execution page")
	}
}

func executionBlockedFixture(t *testing.T, nearExpiry bool) (*managementFixture, *executionBlockedView, func(), string, api.Operation, *atomic.Int64) {
	t.Helper()
	f, head := executionHTTPFixture(t, 2)
	clock := new(atomic.Int64)
	clock.Store(time.Now().UTC().UnixNano())
	if nearExpiry {
		clock.Store(head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30).Add(-time.Second).UnixNano())
	}
	m := f.server.managementHTTP
	m.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	first := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	cursor, err := m.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	gate := &executionBlockedView{collectionExecutionReadView: entry.execution, entered: make(chan struct{}, 16), release: make(chan struct{}), exited: make(chan struct{}, 16)}
	entry.execution = gate
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	return f, gate, release, "/api/v2/operations/" + head.ID + "?cursor=" + url.QueryEscape(first.NextCursor), first, clock
}

func TestCollectionExecutionHTTPBlockedReadAuthorizationAndIndependentReads(t *testing.T) {
	f, gate, release, path, first, _ := executionBlockedFixture(t, false)
	blocked := validationAsyncRead(f, t.Context(), path)
	executionGateWait(t, gate)
	plain, _, _ := strings.Cut(path, "?")
	other := validationReadWait(t, validationAsyncRead(f, t.Context(), plain+"?limit=2"))
	if other.err != nil || other.status != http.StatusOK || !other.tls {
		t.Fatal("blocked execution page stalled another query", other)
	}
	policy := f.authConfig
	policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
	policy.Principals[0].Revoked = true
	replaced := make(chan error, 1)
	go func() { replaced <- f.server.managementHTTP.auth.Replace(policy) }()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("execution page held the authorization policy lock")
	}
	release()
	result := validationReadWait(t, blocked)
	if result.err != nil || result.status != http.StatusUnauthorized || !result.tls {
		t.Fatal("revoked read disclosed execution results", result)
	}
	if result.problem.Detail == first.ID {
		t.Fatal("revoked read exposed the original identity")
	}
}

func TestCollectionExecutionHTTPBlockedReadClockGenerationAndHealth(t *testing.T) {
	for _, scenario := range []string{"result-expiry", "cursor-expiry", "backwards", "generation", "history-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f, gate, release, path, first, clock := executionBlockedFixture(t, scenario == "result-expiry")
			blocked := validationAsyncRead(f, t.Context(), path)
			executionGateWait(t, gate)
			status, code := 410, "cursorExpired"
			switch scenario {
			case "result-expiry":
				cursor, err := f.server.managementHTTP.decodeCursor(first.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				f.server.managementHTTP.mu.Lock()
				expires := f.server.managementHTTP.snapshots[cursor.View].expires
				f.server.managementHTTP.mu.Unlock()
				if !expires.Equal(first.ExecutionResult.Summary.ExpiresAt) {
					t.Fatal("execution cursor outlives retained result")
				}
				clock.Store(first.ExecutionResult.Summary.ExpiresAt.UnixNano())
				code = "operationExpired"
			case "cursor-expiry":
				clock.Add(int64(managementCursorTTL))
			case "backwards":
				clock.Add(-int64(time.Second))
				status, code = 503, "unavailable"
			case "generation":
				if err := f.server.managementHTTP.auth.Replace(f.authConfig); err != nil {
					t.Fatal(err)
				}
				newer := executionHTTPPage(t, f, first.ID, cpra.ExecutionResultPageOptions{Limit: 1})
				plain, _, _ := strings.Cut(path, "?")
				path = plain + "?cursor=" + url.QueryEscape(newer.NextCursor)
			case "history-unavailable":
				if err := f.server.cfg.Store.History().Close(); err != nil {
					t.Fatal(err)
				}
				status, code = 503, "historyUnavailable"
			}
			release()
			result := validationReadWait(t, blocked)
			if result.err != nil || result.status != status || result.problem.Code != code {
				t.Fatal("late execution read ignored authority, time or storage", result)
			}
			if scenario == "generation" {
				current := validationReadWait(t, validationAsyncRead(f, t.Context(), path))
				if current.err != nil || current.status != http.StatusOK {
					t.Fatal("old generation removed a newer execution cursor", current)
				}
			}
		})
	}
}

func TestCollectionExecutionHTTPBlockedReadCancellationAndCapacity(t *testing.T) {
	f, gate, release, path, _, _ := executionBlockedFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	first := validationAsyncRead(f, ctx, path)
	executionGateWait(t, gate)
	cancel()
	result := validationReadWait(t, first)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatal("execution read ignored cancellation", result.err)
	}
	select {
	case <-gate.exited:
	case <-time.After(time.Second):
		t.Fatal("canceled execution page did not return")
	}
	deadline := time.Now().Add(time.Second)
	for len(f.server.managementHTTP.operationReads) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("canceled execution page retained a read slot")
		}
		time.Sleep(time.Millisecond)
	}
	requests := make([]<-chan validationReadResponse, 0, 8)
	for range 8 {
		requests = append(requests, validationAsyncRead(f, t.Context(), path))
		executionGateWait(t, gate)
	}
	over := validationReadWait(t, validationAsyncRead(f, t.Context(), path))
	if over.err != nil || over.status != 429 || over.problem.Code != "readBusy" {
		t.Fatal("execution read concurrency bound failed", over)
	}
	release()
	for _, request := range requests {
		response := validationReadWait(t, request)
		if response.err != nil || response.status != http.StatusOK {
			t.Fatal("admitted execution page failed", response)
		}
	}
}

func TestCollectionExecutionHTTPSharedSnapshotQuotas(t *testing.T) {
	f, head := executionHTTPFixture(t, 3)
	first := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	m := f.server.managementHTTP
	cursor, err := m.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	if entry.operationBytes != collectionExecutionSnapshotBytes || m.operationSnapshotBudget("operator") != managementPrincipalOperationBytes-entry.operationBytes || len(m.operationReservations) != 0 {
		m.mu.Unlock()
		t.Fatal("execution cursor did not charge or release retained-byte quota")
	}
	m.snapshots["byte-quota-fixture"] = managementSnapshot{principal: entry.principal, generation: entry.generation, expires: entry.expires,
		operationBytes: managementPrincipalOperationBytes - entry.operationBytes}
	m.mu.Unlock()
	response := validationReadWait(t, validationAsyncRead(f, t.Context(), "/api/v2/operations/"+head.ID+"?limit=1"))
	if response.err != nil || response.status != 429 || response.problem.Code != "snapshotByteQuota" {
		t.Fatal("execution cursor bypassed byte quota", response)
	}
	page := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1, Cursor: first.NextCursor})
	if len(page.Items) != 1 {
		t.Fatal("retained cursor could not continue at quota")
	}
	m.mu.Lock()
	delete(m.snapshots, "byte-quota-fixture")
	for i := len(m.snapshots); i < managementPrincipalSnapshots; i++ {
		m.snapshots[strings.Repeat("q", i+1)] = managementSnapshot{principal: entry.principal, generation: entry.generation, expires: entry.expires}
	}
	m.mu.Unlock()
	response = validationReadWait(t, validationAsyncRead(f, t.Context(), "/api/v2/operations/"+head.ID+"?limit=1"))
	if response.err != nil || response.status != 429 || response.problem.Code != "snapshotQuota" {
		t.Fatal("execution cursor bypassed count quota", response)
	}
}

type executionCompletedRead struct {
	collectionExecutionReadView
	readDone chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (v *executionCompletedRead) Page(ctx context.Context, after uint64, limit int, at time.Time) (persistence.CollectionExecutionHistoryPage, error) {
	page, err := v.collectionExecutionReadView.Page(ctx, after, limit, at)
	close(v.readDone)
	defer close(v.returned)
	select {
	case <-v.release:
		return page, err
	case <-ctx.Done():
		return persistence.CollectionExecutionHistoryPage{}, ctx.Err()
	}
}

func TestCollectionExecutionHTTPRechecksStorageAfterCursorWait(t *testing.T) {
	f, head := executionHTTPFixture(t, 2)
	first := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	m := f.server.managementHTTP
	cursor, err := m.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	gate := &executionCompletedRead{collectionExecutionReadView: entry.execution, readDone: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	entry.execution = gate
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	request := validationAsyncRead(f, t.Context(), "/api/v2/operations/"+head.ID+"?cursor="+url.QueryEscape(first.NextCursor))
	select {
	case <-gate.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("protected history read did not complete")
	}
	m.mu.Lock()
	release()
	select {
	case <-gate.returned:
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("completed history page did not return")
	}
	err = f.server.cfg.Store.Close()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := validationReadWait(t, request)
	if response.err != nil || response.status != http.StatusServiceUnavailable || response.problem.Code != "collectionUnavailable" {
		t.Fatal("post-read storage failure disclosed a stale execution page", response)
	}
}
