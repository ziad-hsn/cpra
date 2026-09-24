package httpserver

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

type operationBlockedView struct {
	managementOperationReadView
	entered chan struct{}
	release chan struct{}
	exited  chan struct{}
}

func (v *operationBlockedView) ReadPage(ctx context.Context, monitor, after string, limit int) (management.ManagementOperationPage, error) {
	select {
	case v.entered <- struct{}{}:
	case <-ctx.Done():
		return management.ManagementOperationPage{}, ctx.Err()
	}
	defer func() { v.exited <- struct{}{} }()
	select {
	case <-v.release:
		return v.managementOperationReadView.ReadPage(ctx, monitor, after, limit)
	case <-ctx.Done():
		return management.ManagementOperationPage{}, ctx.Err()
	}
}

func operationBlockedFixture(t *testing.T) (*managementFixture, *operationBlockedView, func(), string, *atomic.Int64) {
	t.Helper()
	f := newManagementFixture(t, true)
	operationListCredential(t, f, "one")
	operationListCredential(t, f, "two")
	clock := new(atomic.Int64)
	clock.Store(time.Now().UTC().UnixNano())
	m := f.server.managementHTTP
	m.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	first, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || first.Data.NextCursor == "" {
		t.Fatal("missing initial cursor", err)
	}
	cursor, err := m.decodeCursor(first.Data.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	gate := &operationBlockedView{managementOperationReadView: entry.operations, entered: make(chan struct{}, 16), release: make(chan struct{}), exited: make(chan struct{}, 16)}
	entry.operations = gate
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	return f, gate, release, "/api/v2/operations?cursor=" + url.QueryEscape(first.Data.NextCursor), clock
}

func operationReadEntered(t *testing.T, gate *operationBlockedView) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not enter real retained operation page")
	}
}

func TestManagementOperationReadsDoNotHoldAuthorityOrCursorLocks(t *testing.T) {
	f, gate, release, path, _ := operationBlockedFixture(t)
	blocked := validationAsyncRead(f, t.Context(), path)
	operationReadEntered(t, gate)
	other := validationReadWait(t, validationAsyncRead(f, t.Context(), "/api/v2/operations?limit=2"))
	if other.err != nil || other.status != 200 || !other.tls {
		t.Fatal("blocked operation read stalled another request", other.status, other.err)
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
		t.Fatal("retained Page held policy lock")
	}
	release()
	result := validationReadWait(t, blocked)
	if result.err != nil || result.status != 401 {
		t.Fatal("revoked blocked operation read returned data", result.status, result.err)
	}
}

func TestManagementOperationReadsRecheckClockAndGeneration(t *testing.T) {
	for _, mode := range []string{"expiry", "backwards", "generation"} {
		t.Run(mode, func(t *testing.T) {
			f, gate, release, path, clock := operationBlockedFixture(t)
			blocked := validationAsyncRead(f, t.Context(), path)
			operationReadEntered(t, gate)
			status := 410
			switch mode {
			case "expiry":
				clock.Add(int64(managementCursorTTL))
			case "backwards":
				clock.Add(-int64(time.Second))
				status = 503
			case "generation":
				if err := f.server.managementHTTP.auth.Replace(f.authConfig); err != nil {
					t.Fatal(err)
				}
				newer, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				path = "/api/v2/operations?cursor=" + url.QueryEscape(newer.Data.NextCursor)
			}
			release()
			result := validationReadWait(t, blocked)
			if result.err != nil || result.status != status {
				t.Fatal("blocked read crossed clock or policy boundary", result.status, result.err)
			}
			if mode == "generation" {
				result = validationReadWait(t, validationAsyncRead(f, t.Context(), path))
				if result.err != nil || result.status != 200 {
					t.Fatal("old read removed newer generation cursor", result.status, result.err)
				}
			}
		})
	}
}

func TestManagementOperationReadsBoundConcurrentWorkAndCancel(t *testing.T) {
	f, gate, release, path, _ := operationBlockedFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	request := validationAsyncRead(f, ctx, path)
	operationReadEntered(t, gate)
	cancel()
	result := validationReadWait(t, request)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatal("client cancellation lost", result.err)
	}
	select {
	case <-gate.exited:
	case <-time.After(time.Second):
		t.Fatal("page read ignored cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for len(f.server.managementHTTP.operationReads) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("canceled operation read retained admission slot")
		}
		time.Sleep(time.Millisecond)
	}
	requests := make([]<-chan validationReadResponse, 0, 8)
	for range 8 {
		requests = append(requests, validationAsyncRead(f, t.Context(), path))
		operationReadEntered(t, gate)
	}
	result = validationReadWait(t, validationAsyncRead(f, t.Context(), path))
	if result.err != nil || result.status != 429 || result.problem.Code != "readBusy" {
		t.Fatal("concurrent operation reads were unbounded", result.status, result.err)
	}
	release()
	for _, request := range requests {
		result = validationReadWait(t, request)
		if result.err != nil || result.status != 200 {
			t.Fatal("admitted cursor read failed", result.status, result.err)
		}
	}
}

func TestManagementOperationReadsReserveCaptureBytesAndReleaseFailures(t *testing.T) {
	f := newManagementFixture(t, true)
	operationListCredential(t, f, "one")
	operationListCredential(t, f, "two")
	m := f.server.managementHTTP
	authority := operationReadAuthority{principal: "operator", generation: m.auth.Generation(), collections: true}
	// Pause a completed real snapshot between page construction and publication.
	// Its reservation must count even though no cursor has been made available.
	read, err := m.readOperations(t.Context(), url.Values{"limit": {"1"}}, authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.releaseOperationReservation(read.reservation) })
	response, _ := f.request(t, "GET", "/api/v2/operations?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 429 {
		t.Fatal("detached snapshot was absent from allocation quota", response.StatusCode)
	}
	m.releaseOperationReservation(read.reservation)
	if _, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1}); err != nil {
		t.Fatal("released reservation did not restore capacity", err)
	}
	response, _ = f.request(t, "GET", "/api/v2/operations?monitorID=invalid%00monitor", managementOperatorToken, nil, nil)
	if response.StatusCode != 422 && response.StatusCode != 400 {
		t.Fatal("bad filter accepted", response.StatusCode)
	}
	m.mu.Lock()
	reserved := len(m.operationReservations)
	m.mu.Unlock()
	if reserved != 0 {
		t.Fatal("failed page construction retained byte reservation")
	}
}
