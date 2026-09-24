package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type validationBlockedView struct {
	collectionValidationReadView
	entered chan struct{}
	release chan struct{}
	exited  chan struct{}
}

func (v *validationBlockedView) Page(ctx context.Context, after uint64, limit int, at time.Time) (persistence.CollectionValidationHistoryPage, error) {
	select {
	case v.entered <- struct{}{}:
	case <-ctx.Done():
		return persistence.CollectionValidationHistoryPage{}, ctx.Err()
	}
	defer func() { v.exited <- struct{}{} }()
	select {
	case <-v.release:
		return v.collectionValidationReadView.Page(ctx, after, limit, at)
	case <-ctx.Done():
		return persistence.CollectionValidationHistoryPage{}, ctx.Err()
	}
}

type validationReadResponse struct {
	status  int
	problem api.Problem
	tls     bool
	err     error
}

func validationAsyncRead(f *managementFixture, ctx context.Context, path string) <-chan validationReadResponse {
	done := make(chan validationReadResponse, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.http.URL+path, nil)
		if err != nil {
			done <- validationReadResponse{err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+managementOperatorToken)
		response, err := f.http.Client().Do(req)
		if err != nil {
			done <- validationReadResponse{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		var problem api.Problem
		if response.StatusCode != 200 && err == nil {
			err = json.Unmarshal(body, &problem)
		}
		done <- validationReadResponse{status: response.StatusCode, problem: problem, tls: response.TLS != nil, err: err}
	}()
	return done
}
func validationReadWait(t *testing.T, done <-chan validationReadResponse) validationReadResponse {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("TLS result read did not complete")
		return validationReadResponse{}
	}
}
func validationGateWait(t *testing.T, gate *validationBlockedView) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("TLS request did not enter retained result page")
	}
}

func validationBlockedFixture(t *testing.T, nearExpiry bool) (*managementFixture, *validationBlockedView, func(), string, api.ValidationResultPage, *atomic.Int64) {
	t.Helper()
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	original := validationHTTPOperation(t, f, preflightMonitor("one", "https://one.example.test"), preflightMonitor("two", "https://two.example.test"))
	if _, err := f.sdk.Operations.Validate(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	worker := validationHTTPWorker(t, f)
	path := "/api/v2/operations/" + original.ID + "/validation"
	first := validationHTTPResult(t, f, path+"?limit=1")
	worker.BeginStop()
	if err := worker.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock := new(atomic.Int64)
	clock.Store(time.Now().UTC().UnixNano())
	if nearExpiry {
		clock.Store(first.Summary.ExpiresAt.Add(-time.Second).UnixNano())
	}
	m := f.server.managementHTTP
	m.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	if nearExpiry {
		first = validationHTTPResult(t, f, path+"?limit=1")
	}
	cursor, err := m.decodeCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	gate := &validationBlockedView{collectionValidationReadView: entry.validation, entered: make(chan struct{}, 16), release: make(chan struct{}), exited: make(chan struct{}, 16)}
	entry.validation = gate
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	return f, gate, release, path + "?cursor=" + url.QueryEscape(first.NextCursor), first, clock
}

func TestCollectionValidationHTTPBlockedReadDoesNotHoldPolicyOrCursorLocks(t *testing.T) {
	f, gate, release, path, _, _ := validationBlockedFixture(t, false)
	blocked := validationAsyncRead(f, t.Context(), path)
	validationGateWait(t, gate)
	// A fresh query uses another real view while the retained Page is blocked.
	plain, _, _ := strings.Cut(path, "?")
	other := validationReadWait(t, validationAsyncRead(f, t.Context(), plain+"?limit=2"))
	if other.err != nil || other.status != 200 || !other.tls {
		t.Fatal("blocked read stalled another cursor", other.status, other.err)
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
		t.Fatal("history read held the authorization policy lock")
	}
	release()
	result := validationReadWait(t, blocked)
	if result.err != nil || result.status != 401 || !result.tls {
		t.Fatal("revoked blocked read disclosed result", result.status, result.err)
	}
}

func TestCollectionValidationHTTPBlockedReadClockAndGeneration(t *testing.T) {
	for _, scenario := range []string{"result-expiry", "cursor-expiry", "backwards", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			f, gate, release, path, first, clock := validationBlockedFixture(t, scenario == "result-expiry")
			blocked := validationAsyncRead(f, t.Context(), path)
			validationGateWait(t, gate)
			status := 410
			code := "cursorExpired"
			switch scenario {
			case "result-expiry":
				cursor, err := f.server.managementHTTP.decodeCursor(first.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				f.server.managementHTTP.mu.Lock()
				expires := f.server.managementHTTP.snapshots[cursor.View].expires
				f.server.managementHTTP.mu.Unlock()
				if !expires.Equal(first.Summary.ExpiresAt) {
					t.Fatal("cursor lifetime exceeds result retention")
				}
				clock.Store(first.Summary.ExpiresAt.UnixNano())
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
				plain, _, _ := strings.Cut(path, "?")
				newer := validationHTTPResult(t, f, plain+"?limit=1")
				// Keep a new-generation cursor live while the old read finishes.
				path = plain + "?cursor=" + url.QueryEscape(newer.NextCursor)
			}
			release()
			result := validationReadWait(t, blocked)
			if result.err != nil || result.status != status || result.problem.Code != code {
				t.Fatal("late read ignored clock or policy", result.status, result.problem.Code, result.err)
			}
			if scenario == "generation" {
				next := validationReadWait(t, validationAsyncRead(f, t.Context(), path))
				if next.err != nil || next.status != 200 {
					t.Fatal("old read destroyed newer cursor", next.status, next.err)
				}
			}
		})
	}
}

func TestCollectionValidationHTTPBlockedReadCancellationAndCapacity(t *testing.T) {
	f, gate, release, path, _, _ := validationBlockedFixture(t, false)
	ctx, cancel := context.WithCancel(t.Context())
	first := validationAsyncRead(f, ctx, path)
	validationGateWait(t, gate)
	cancel()
	result := validationReadWait(t, first)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatal("client cancellation was ignored", result.err)
	}
	select {
	case <-gate.exited:
	case <-time.After(time.Second):
		t.Fatal("handler did not cancel the page read")
	}
	// Ensure the canceled handler relinquishes its single concurrency slot.
	deadline := time.Now().Add(time.Second)
	for len(f.server.managementHTTP.validationReads) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("canceled read retained admission slot")
		}
		time.Sleep(time.Millisecond)
	}
	requests := make([]<-chan validationReadResponse, 0, 8)
	for range 8 {
		requests = append(requests, validationAsyncRead(f, t.Context(), path))
		validationGateWait(t, gate)
	}
	over := validationReadWait(t, validationAsyncRead(f, t.Context(), path))
	if over.err != nil || over.status != 429 || over.problem.Code != "readBusy" {
		t.Fatal("in-flight read bound failed", over.status, over.problem.Code, over.err)
	}
	release()
	for _, request := range requests {
		response := validationReadWait(t, request)
		if response.err != nil || response.status != 200 {
			t.Fatal("admitted read lost original page", response.status, response.err)
		}
	}
}

type validationWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *validationWaitingContext) Done() <-chan struct{} {
	// The mutex helper consults Done only after an unsuccessful TryLock.
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCollectionValidationCursorWaitCancellationAndGenerationPruning(t *testing.T) {
	m := &managementHTTP{snapshots: make(map[string]managementSnapshot)}
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &validationWaitingContext{Context: base, waiting: make(chan struct{})}
	m.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- m.lockSnapshots(ctx) }()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("cursor read did not reach the mutex wait")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("cursor mutex wait ignored cancellation")
	}
	now := time.Now()
	m.snapshots["old"] = managementSnapshot{principal: "actor", generation: 1, expires: now.Add(time.Hour)}
	m.snapshots["new"] = managementSnapshot{principal: "actor", generation: 3, expires: now.Add(time.Hour)}
	m.snapshots["other"] = managementSnapshot{principal: "other", generation: 1, expires: now.Add(time.Hour)}
	m.pruneSnapshots(now, "actor", 2)
	_, old := m.snapshots["old"]
	_, newer := m.snapshots["new"]
	_, other := m.snapshots["other"]
	m.mu.Unlock()
	if old || !newer || !other {
		t.Fatal("stale generation pruned newer or unrelated context")
	}
}
