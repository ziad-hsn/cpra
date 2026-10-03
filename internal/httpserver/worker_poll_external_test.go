//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type observedPollStore struct {
	*persistence.Store
	entered chan time.Duration
}

func (s *observedPollStore) WaitWorkerPoll(ctx context.Context, a persistence.WorkerAuthority, r persistence.WorkerPollRequest) error {
	deadline, _ := ctx.Deadline()
	select {
	case s.entered <- time.Until(deadline):
	default:
	}
	return s.Store.WaitWorkerPoll(ctx, a, r)
}

func TestWorkerPollHTTPWaitSDKArrival(t *testing.T) {
	f := newWorkerSessionFixture(t)
	f.request.WaitSeconds = 25
	observed := &observedPollStore{Store: f.store, entered: make(chan time.Duration, 1)}
	f.handler.store = observed
	var admissions atomic.Int64
	f.handler.admit = func(ctx context.Context, fn func() error) error {
		admissions.Add(1)
		return fn()
	}
	client := workerHTTPClient(t, f)
	type result struct {
		value *api.Assignments
		err   error
	}
	completed := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		value, err := client.Poll(ctx, f.request)
		completed <- result{value, err}
	}()
	select {
	case remaining := <-observed.entered:
		if remaining <= 25*time.Second || remaining > 30*time.Second {
			t.Fatal("long poll inherited the short body deadline", remaining)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not enter its bounded wait")
	}
	if admissions.Load() != 0 {
		t.Fatal("long poll held the mutation admission gate")
	}
	workerSessionProblem(t, f.call(t, f.request), 429, "workerSessionQuota")
	execution := queueWorkerHTTPCheck(t, f)
	var reply result
	select {
	case reply = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("newly admitted work did not wake polling")
	}
	if reply.err != nil || reply.value == nil || len(reply.value.Items) != 1 || reply.value.Items[0].ExecutionID != execution.Intent.ID {
		t.Fatal("arriving work was not returned", reply.err)
	}
	if admissions.Load() != 1 {
		t.Fatal("one long poll performed multiple durable admissions")
	}
	retryCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	replayed, err := client.Poll(retryCtx, f.request)
	if err != nil || !reflect.DeepEqual(reply.value, replayed) {
		t.Fatal("lost poll reply did not reconcile immediately", err)
	}
	record, ok, err := f.store.WorkerExecution(t.Context(), execution.Intent.ID)
	if err != nil || !ok || record.Lifecycle == nil || record.Lifecycle.Phase != "offered" {
		t.Fatal("polling granted execution or lost the original offer", err)
	}
}

func TestWorkerPollHTTPEmptyWaitAndCancellation(t *testing.T) {
	for _, mode := range []string{"empty", "maximum", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkerSessionFixture(t)
			client := workerHTTPClient(t, f)
			f.request.WaitSeconds = 1
			if mode == "maximum" {
				f.request.WaitSeconds = 25
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Duration(f.request.WaitSeconds+10)*time.Second)
			defer cancel()
			if mode == "cancel" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			started := time.Now()
			response, err := client.Poll(ctx, f.request)
			if mode != "cancel" {
				if err != nil || response == nil || len(response.Items) != 0 || time.Since(started) < time.Duration(f.request.WaitSeconds)*time.Second {
					t.Fatal("empty poll did not wait before committing", err)
				}
				retryCtx, stop := context.WithTimeout(t.Context(), 500*time.Millisecond)
				defer stop()
				replayed, err := client.Poll(retryCtx, f.request)
				if err != nil || !reflect.DeepEqual(response, replayed) {
					t.Fatal("empty reply was not replayed immediately", err)
				}
				return
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("client cancellation was lost", err)
			}
			// Wait for the server to observe disconnect and release its request
			// reservation. A fresh request with the same nonce and different wait
			// proves that cancellation did not freeze an empty response.
			deadline := time.Now().Add(2 * time.Second)
			for !f.handler.reserve(f.uid) {
				if time.Now().After(deadline) {
					t.Fatal("canceled poll retained admission")
				}
				time.Sleep(time.Millisecond)
			}
			f.handler.release(f.uid)
			f.request.WaitSeconds = 0
			if _, err := client.Poll(t.Context(), f.request); err != nil {
				t.Fatal("canceled wait committed a hidden session", err)
			}
		})
	}
}
