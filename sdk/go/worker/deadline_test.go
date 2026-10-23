//go:build externaljobs

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestNotificationStartDeadlineReplacesOfferUpperBound(t *testing.T) {
	c := testConfig(t)
	a := assignment("waiting-notification", "notification")
	a.Deadline = time.Now().Add(2 * time.Minute).UTC()
	exact := a.Deadline.Add(-time.Minute)
	starts, calls := 0, 0
	c.Client = &protocolFuncs{fakeProtocol: newFake(), start: func(_ context.Context, request api.StartRequest) (*api.StartResponse, error) {
		starts++
		reply := startReply(request, api.StartDispositionGranted)
		reply.Deadline = exact
		return reply, nil
	}}
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if r != nil {
			_ = r.Close()
		}
	})
	handler := func(ctx context.Context, _ Job) (api.Outcome, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(exact) {
			t.Fatal("handler did not receive the committed execution deadline")
		}
		rows, err := r.journal.records()
		if err != nil || len(rows) != 1 || rows[0].State != "started" || !rows[0].Deadline.Equal(exact) {
			t.Fatal("exact deadline was not persisted before invocation", err)
		}
		return api.Outcome{Status: "delivered"}, nil
	}
	r.execute(t.Context(), a, handler, func(err error) { t.Fatal(err) })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = New(c)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.journal.records()
	if err != nil || len(rows) != 1 || rows[0].State != "outcome" || !rows[0].Deadline.Equal(exact) {
		t.Fatal("restart did not retain the execution deadline and outcome", err)
	}
	// The retained execution cannot obtain another grant or invoke again, even
	// when a repeated assignment carries a later offer upper bound.
	a.Deadline = a.Deadline.Add(time.Hour)
	r.execute(t.Context(), a, handler, func(err error) { t.Fatal(err) })
	if calls != 1 || starts != 1 {
		t.Fatal("repeated assignment invoked or requested another start", calls, starts)
	}
}
