package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type snapshotReadGate struct{ entered, release, exited chan struct{} }
type snapshotBlockedView[T any] struct {
	snapshotPageView[T]
	gate *snapshotReadGate
}

func (v *snapshotBlockedView[T]) Page(ctx context.Context, scope, after string, limit int) ([]T, string, error) {
	select {
	case v.gate.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { v.gate.exited <- struct{}{} }()
	select {
	case <-v.gate.release:
		return v.snapshotPageView.Page(ctx, scope, after, limit)
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}
func snapshotGateEntered(t *testing.T, gate *snapshotReadGate) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request never entered retained snapshot Page")
	}
}
func snapshotBlockedFixture(t *testing.T, kind string) (*managementFixture, *snapshotReadGate, func(), string, string, *atomic.Int64) {
	t.Helper()
	f := newManagementFixture(t, true)
	for _, id := range []string{"one", "two"} {
		_, m, g := configureControlMonitor(t, f, id)
		controlPulse(t, f, m, g, "failure")
	}
	paths := map[string]string{"Monitor": "/api/v2/monitors?limit=1", "Incident": "/api/v2/incidents?limit=1", "Action": "/api/v2/actions?limit=1", "History": "/api/v2/history?monitorID=one&limit=1"}
	base := paths[kind]
	m := f.server.managementHTTP
	clock := new(atomic.Int64)
	clock.Store(time.Now().UTC().UnixNano())
	m.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	response, raw := f.request(t, "GET", base, managementOperatorToken, nil, nil)
	var page struct {
		NextCursor string `json:"nextCursor"`
	}
	if response.StatusCode != 200 || json.Unmarshal(raw, &page) != nil || page.NextCursor == "" {
		t.Fatal("missing real first-page cursor", response.StatusCode, string(raw))
	}
	cursor, err := m.decodeCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	gate := &snapshotReadGate{entered: make(chan struct{}, 16), release: make(chan struct{}), exited: make(chan struct{}, 16)}
	m.mu.Lock()
	entry := m.snapshots[cursor.View]
	switch kind {
	case "Monitor":
		entry.view = &snapshotBlockedView[api.Resource]{entry.view, gate}
	case "Incident":
		entry.incidents = &snapshotBlockedView[api.Incident]{entry.incidents, gate}
	case "Action":
		entry.actions = &snapshotBlockedView[api.Action]{entry.actions, gate}
	case "History":
		entry.history = &snapshotBlockedView[api.Event]{entry.history, gate}
	}
	m.snapshots[cursor.View] = entry
	m.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	return f, gate, release, base + "&cursor=" + url.QueryEscape(page.NextCursor), base, clock
}

func TestManagementSnapshotReadsAllowConcurrentRequestsAndRevocation(t *testing.T) {
	for _, kind := range []string{"Monitor", "Incident", "Action", "History"} {
		t.Run(kind, func(t *testing.T) {
			f, gate, release, path, base, _ := snapshotBlockedFixture(t, kind)
			pending := validationAsyncRead(f, t.Context(), path)
			snapshotGateEntered(t, gate)
			other := validationReadWait(t, validationAsyncRead(f, t.Context(), base))
			if other.err != nil || other.status != 200 {
				t.Fatal("snapshot Page stalled another request", other.status, other.err)
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
				t.Fatal("snapshot Page held policy lock during revocation")
			}
			release()
			got := validationReadWait(t, pending)
			if got.err != nil || got.status != 401 {
				t.Fatal("revoked request returned snapshot data", got.status, got.err)
			}
		})
	}
}
func TestManagementSnapshotReadsRecheckExpiryAndGeneration(t *testing.T) {
	for _, kind := range []string{"Monitor", "Incident", "Action", "History"} {
		for _, mode := range []string{"expiry", "backwards", "generation"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f, gate, release, path, base, clock := snapshotBlockedFixture(t, kind)
				pending := validationAsyncRead(f, t.Context(), path)
				snapshotGateEntered(t, gate)
				want := 410
				switch mode {
				case "expiry":
					clock.Add(int64(managementCursorTTL))
				case "backwards":
					clock.Add(-int64(time.Second))
					want = 503
				case "generation":
					if err := f.server.managementHTTP.auth.Replace(f.authConfig); err != nil {
						t.Fatal(err)
					}
					response, raw := f.request(t, "GET", base, managementOperatorToken, nil, nil)
					var page struct {
						NextCursor string `json:"nextCursor"`
					}
					if response.StatusCode != 200 || json.Unmarshal(raw, &page) != nil || page.NextCursor == "" {
						t.Fatal("new generation cursor missing")
					}
					path = base + "&cursor=" + url.QueryEscape(page.NextCursor)
				}
				release()
				got := validationReadWait(t, pending)
				if got.err != nil || got.status != want {
					t.Fatal("in-flight snapshot crossed authority or clock boundary", got.status, got.err)
				}
				if mode == "generation" {
					got = validationReadWait(t, validationAsyncRead(f, t.Context(), path))
					if got.err != nil || got.status != 200 {
						t.Fatal("old request removed new generation cursor", got.status, got.err)
					}
				}
			})
		}
	}
}
func TestManagementSnapshotReadsCancelAndBoundConcurrency(t *testing.T) {
	for _, kind := range []string{"Monitor", "Incident", "Action", "History"} {
		t.Run(kind, func(t *testing.T) {
			f, gate, release, path, _, _ := snapshotBlockedFixture(t, kind)
			ctx, cancel := context.WithCancel(t.Context())
			pending := validationAsyncRead(f, ctx, path)
			snapshotGateEntered(t, gate)
			cancel()
			got := validationReadWait(t, pending)
			if !errors.Is(got.err, context.Canceled) {
				t.Fatal("client cancellation lost", got.err)
			}
			select {
			case <-gate.exited:
			case <-time.After(time.Second):
				t.Fatal("snapshot Page ignored cancellation")
			}
			deadline := time.Now().Add(time.Second)
			for len(f.server.managementHTTP.snapshotReads) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("canceled read retained admission slot")
				}
				time.Sleep(time.Millisecond)
			}
			var requests []<-chan validationReadResponse
			for range 8 {
				requests = append(requests, validationAsyncRead(f, t.Context(), path))
				snapshotGateEntered(t, gate)
			}
			got = validationReadWait(t, validationAsyncRead(f, t.Context(), path))
			if got.err != nil || got.status != 429 || got.problem.Code != "readBusy" {
				t.Fatal("snapshot reads were unbounded", got.status, got.err)
			}
			release()
			for _, request := range requests {
				got = validationReadWait(t, request)
				if got.err != nil || got.status != 200 {
					t.Fatal("admitted snapshot read failed", got.status, got.err)
				}
			}
		})
	}
}
func TestManagementSnapshotCaptureReservationsCountAndRelease(t *testing.T) {
	f := newManagementFixture(t, true)
	m := f.server.managementHTTP
	reads := make([]*snapshotRead, 0, managementPrincipalSnapshots)
	for range managementPrincipalSnapshots {
		read, err := m.beginSnapshotRead(t.Context(), "Monitor", url.Values{}, "operator", m.auth.Generation())
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, read)
	}
	response, _ := f.request(t, "GET", "/api/v2/monitors", managementOperatorToken, nil, nil)
	if response.StatusCode != 429 {
		t.Fatal("unfinished captures escaped cursor quota", response.StatusCode)
	}
	for _, read := range reads {
		m.releaseSnapshotRead(read)
	}
	response, _ = f.request(t, "GET", "/api/v2/monitors", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal("released reservations kept cursor quota", response.StatusCode)
	}
	m.mu.Lock()
	retained := len(m.snapshots)
	m.mu.Unlock()
	if retained != 0 {
		t.Fatal("terminal first page retained a reservation")
	}
}
