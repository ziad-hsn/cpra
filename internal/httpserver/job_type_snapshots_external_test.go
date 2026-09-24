//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func jobTypeSnapshotHTTP(t *testing.T) (*managementHTTP, *atomic.Int64) {
	t.Helper()
	clock := new(atomic.Int64)
	clock.Store(time.Now().UTC().UnixNano())
	m := &managementHTTP{snapshots: make(map[string]managementSnapshot), cursorReady: true,
		now: func() time.Time { return time.Unix(0, clock.Load()).UTC() }}
	t.Cleanup(func() {
		m.mu.Lock()
		m.pruneSnapshots(m.now().Add(2*managementCursorTTL), "", 0)
		m.mu.Unlock()
	})
	return m, clock
}

func assertJobTypeSnapshotFailure(t *testing.T, err error, status int, code string) {
	t.Helper()
	var failure *managementHTTPError
	if !errors.As(err, &failure) || failure.status != status || failure.code != code {
		t.Fatalf("expected %d/%s, got %v", status, code, err)
	}
}

func TestJobTypeSnapshotReservationQuotas(t *testing.T) {
	m, _ := jobTypeSnapshotHTTP(t)
	var reads []*snapshotRead
	t.Cleanup(func() {
		for _, read := range reads {
			m.releaseSnapshotRead(read)
		}
	})
	for _, principal := range []string{"one", "one", "two", "two"} {
		read, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{}, principal, 1)
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, read)
	}
	if got := m.jobTypeSnapshotBytes.Load(); got != maxJobTypeSnapshotBytes {
		t.Fatalf("capture reservations: %d", got)
	}
	for _, principal := range []string{"one", "three"} {
		_, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{}, principal, 1)
		assertJobTypeSnapshotFailure(t, err, 429, "snapshotByteQuota")
		if len(m.snapshots) != 4 || m.jobTypeSnapshotBytes.Load() != maxJobTypeSnapshotBytes {
			t.Fatal("rejected capture changed retained accounting")
		}
	}
	const captured = int64(4096)
	reservation := reads[0].entry.jobTypeReservation
	if err := reservation.shrink(captured); err != nil {
		t.Fatal(err)
	}
	want := int64(maxJobTypeSnapshotBytes-jobTypeSnapshotReservationBytes) + captured
	if m.jobTypeSnapshotBytes.Load() != want || m.jobTypePrincipalBytes["one"].Load() != jobTypeSnapshotReservationBytes+captured {
		t.Fatal("capture shrink lost global or principal accounting")
	}
	for _, size := range []int64{-1, jobTypeSnapshotReservationBytes + 1} {
		if err := reservation.shrink(size); err == nil || m.jobTypeSnapshotBytes.Load() != want || reservation.bytes.Load() != captured {
			t.Fatal("invalid captured size changed accounting", err)
		}
	}
	for _, read := range reads {
		m.releaseSnapshotRead(read)
	}
	reads = nil
	if len(m.snapshots) != 0 || m.jobTypeSnapshotBytes.Load() != 0 {
		t.Fatal("failed/unpublished captures retained quota")
	}
	next, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{}, "new", 1)
	if err != nil {
		t.Fatal(err)
	}
	reads = append(reads, next)
	if len(m.jobTypePrincipalBytes) != 1 || m.jobTypePrincipalBytes["new"] == nil {
		t.Fatal("zero-byte principal entries were retained")
	}
}

// A cursor map and every active request have separate ownership. Expiry may
// remove the map entry while an admitted request still holds encrypted state.
func TestJobTypeSnapshotActiveReaderRetainsPrunedCharge(t *testing.T) {
	for _, prune := range []string{"cursor", "observation"} {
		t.Run(prune, func(t *testing.T) {
			m, clock := jobTypeSnapshotHTTP(t)
			initial, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{"limit": {"1"}}, "reader", 1)
			if err != nil {
				t.Fatal(err)
			}
			const captured = int64(8192)
			if err = initial.entry.jobTypeReservation.shrink(captured); err != nil {
				t.Fatal(err)
			}
			initial.after = "one"
			if err = m.publishSnapshotRead(t.Context(), initial); err != nil {
				t.Fatal(err)
			}
			token := m.encodeCursor(managementCursor{View: initial.cursor.View, After: initial.after})
			m.releaseSnapshotRead(initial)
			active, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{"cursor": {token}}, "reader", 1)
			if err != nil {
				t.Fatal(err)
			}
			clock.Add(int64(managementCursorTTL))
			if prune == "cursor" {
				m.mu.Lock()
				m.pruneSnapshots(m.now(), "reader", 1)
				m.mu.Unlock()
			} else {
				// System has no observations without configured systems; this still
				// exercises the production observation cursor's shared prune path.
				_, err = (&Server{}).observationPage(m, url.Values{}, "System", "other", 1)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(m.snapshots) != 0 || m.jobTypeSnapshotBytes.Load() != captured || active.entry.jobTypeReservation.refs.Load() != 1 {
				t.Fatal("cursor pruning released an active request's charge")
			}
			assertJobTypeSnapshotFailure(t, m.publishSnapshotRead(t.Context(), active), 410, "cursorExpired")
			other, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{}, "other", 1)
			if err != nil {
				t.Fatal(err)
			}
			if m.jobTypeSnapshotBytes.Load() != captured+jobTypeSnapshotReservationBytes {
				t.Fatal("new capture ignored the expired active request")
			}
			m.releaseSnapshotRead(active)
			if m.jobTypeSnapshotBytes.Load() != jobTypeSnapshotReservationBytes || m.jobTypePrincipalBytes["reader"].Load() != 0 {
				t.Fatal("active completion failed to release its own charge")
			}
			m.releaseSnapshotRead(other)
			if m.jobTypeSnapshotBytes.Load() != 0 {
				t.Fatal("snapshot bytes leaked after both owners exited")
			}
		})
	}
}

type jobTypeSnapshotBlockedPage struct {
	entered chan struct{}
	release chan struct{}
}

func (v *jobTypeSnapshotBlockedPage) Page(ctx context.Context, _, _ string, _ int) ([]api.JobType, string, error) {
	close(v.entered)
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-v.release:
		return []api.JobType{}, "", nil
	}
}

func TestJobTypeSnapshotBlockedPageAuthorityAndLifetime(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "generation", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagementFixture(t, true)
			m := f.server.managementHTTP
			clock := new(atomic.Int64)
			clock.Store(time.Now().UTC().UnixNano())
			m.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			initial, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{"limit": {"1"}}, "operator", m.auth.Generation())
			if err != nil {
				t.Fatal(err)
			}
			const captured = int64(4096)
			if err = initial.entry.jobTypeReservation.shrink(captured); err != nil {
				t.Fatal(err)
			}
			view := &jobTypeSnapshotBlockedPage{entered: make(chan struct{}), release: make(chan struct{})}
			initial.entry.jobTypes, initial.after = view, "one"
			if err = m.publishSnapshotRead(t.Context(), initial); err != nil {
				t.Fatal(err)
			}
			token := m.encodeCursor(managementCursor{View: initial.cursor.View, After: initial.after})
			m.releaseSnapshotRead(initial)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "https://cpra.example/api/v2/job-types", nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+managementOperatorToken)
			done := make(chan error, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				done <- m.withSnapshotRead(r, "ListJobTypes", "JobType", url.Values{"cursor": {token}}, func(ctx context.Context, read *snapshotRead) error {
					_, after, pageErr := read.entry.jobTypes.Page(ctx, "JobType", read.cursor.After, read.entry.limit)
					read.after = after
					return pageErr
				})
			}()
			var once sync.Once
			release := func() { once.Do(func() { close(view.release) }) }
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-joined:
				case <-time.After(2 * time.Second):
					t.Error("snapshot test goroutine did not join")
				}
			})
			select {
			case <-view.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("Page did not start")
			}
			if !m.mu.TryLock() {
				t.Fatal("blocked Page held cursor mutex")
			}
			m.mu.Unlock()
			// A separate capture can proceed while one request owns a slow page.
			other, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{}, "other", m.auth.Generation())
			if err != nil {
				t.Fatal("blocked Page prevented capture", err)
			}
			m.releaseSnapshotRead(other)
			switch mode {
			case "cancel":
				cancel()
			case "revoke", "generation":
				policy := f.authConfig
				policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
				if mode == "revoke" {
					policy.Principals[0].Revoked = true
				}
				replaced := make(chan error, 1)
				go func() { replaced <- m.auth.Replace(policy) }()
				select {
				case err := <-replaced:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("blocked Page held authorization lock")
				}
			case "expiry":
				clock.Add(int64(managementCursorTTL))
				m.mu.Lock()
				m.pruneSnapshots(m.now(), "operator", m.auth.Generation())
				m.mu.Unlock()
				if m.jobTypeSnapshotBytes.Load() != captured {
					t.Fatal("expiry released a blocked page's charge")
				}
			}
			release()
			var result error
			select {
			case result = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("snapshot read did not return")
			}
			switch mode {
			case "cancel":
				if !errors.Is(result, context.Canceled) {
					t.Fatal("cancellation lost", result)
				}
			case "revoke":
				if !errors.Is(result, httpauth.ErrUnauthorized) {
					t.Fatal("revoked page returned data", result)
				}
			default:
				assertJobTypeSnapshotFailure(t, result, 410, "cursorExpired")
			}
			m.mu.Lock()
			m.pruneSnapshots(m.now().Add(2*managementCursorTTL), "", 0)
			m.mu.Unlock()
			if m.jobTypeSnapshotBytes.Load() != 0 || len(m.snapshotReads) != 0 {
				t.Fatal("read completion retained reservation or admission slot")
			}
		})
	}
}

func TestJobTypeSnapshotCursorBoundsAndOwnership(t *testing.T) {
	for _, limit := range []string{"", "1", "500", "0", "501", "not-an-integer"} {
		t.Run("limit/"+limit, func(t *testing.T) {
			m, _ := jobTypeSnapshotHTTP(t)
			read, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{"limit": {limit}}, "reader", 1)
			if limit == "0" || limit == "501" || limit == "not-an-integer" {
				assertJobTypeSnapshotFailure(t, err, 400, "invalidLimit")
				if m.jobTypeSnapshotBytes.Load() != 0 || len(m.snapshots) != 0 {
					t.Fatal("invalid limit reserved capture capacity")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer m.releaseSnapshotRead(read)
			if limit == "" && read.entry.limit != 100 {
				t.Fatal("default page limit changed")
			}
		})
	}
	for _, mode := range []string{"repeat", "principal", "kind", "generation", "limit", "tamper"} {
		t.Run("continuation/"+mode, func(t *testing.T) {
			m, _ := jobTypeSnapshotHTTP(t)
			first, err := m.beginSnapshotRead(t.Context(), "JobType", url.Values{"limit": {"1"}}, "reader", 1)
			if err != nil {
				t.Fatal(err)
			}
			const cost = int64(1024)
			if err = first.entry.jobTypeReservation.shrink(cost); err != nil {
				t.Fatal(err)
			}
			first.after = "original-boundary"
			if err = m.publishSnapshotRead(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			query := url.Values{"cursor": {m.encodeCursor(managementCursor{View: first.cursor.View, After: first.after})}}
			m.releaseSnapshotRead(first)
			kind, principal, generation := "JobType", "reader", uint64(1)
			switch mode {
			case "principal":
				principal = "another-reader"
			case "kind":
				kind = "Monitor"
			case "generation":
				generation++
			case "limit":
				query.Set("limit", "2")
			case "tamper":
				query.Set("cursor", "!"+query.Get("cursor"))
			}
			read, err := m.beginSnapshotRead(t.Context(), kind, query, principal, generation)
			if mode == "repeat" {
				if err != nil {
					t.Fatal(err)
				}
				m.releaseSnapshotRead(read)
				repeated, err := m.beginSnapshotRead(t.Context(), kind, query, principal, generation)
				if err != nil {
					t.Fatal(err)
				}
				defer m.releaseSnapshotRead(repeated)
				if repeated.cursor != read.cursor || repeated.entry.jobTypeReservation != read.entry.jobTypeReservation || m.jobTypeSnapshotBytes.Load() != cost {
					t.Fatal("repeated cursor moved or duplicated its retained capture")
				}
				return
			}
			status, code := 410, "cursorExpired"
			if mode == "limit" {
				status, code = 400, "cursorMismatch"
			} else if mode == "tamper" {
				status, code = 400, "invalidCursor"
			}
			assertJobTypeSnapshotFailure(t, err, status, code)
		})
	}
}
