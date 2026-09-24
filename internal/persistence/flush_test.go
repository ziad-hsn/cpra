package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFlushJoinsEarlierAdmittedWritesAfterLostReply(t *testing.T) {
	s, start := dormantCatalogStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	m := Monitor{ID: "before-barrier", Revision: "one", Name: "Before barrier", Policy: Policy{Interval: time.Minute}}
	go func() {
		_, err := s.Submit(ctx, []Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()}})
		result <- err
	}()
	waitBudgetCondition(t, "accepted write", func() bool { return len(s.requests) == 1 })
	cancel()
	if err := <-result; !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("admitted cancelled call lost uncertainty", err)
	}
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush(context.Background()) }()
	waitBudgetCondition(t, "queued barrier", func() bool { return len(s.requests) == 2 })
	select {
	case err := <-flushed:
		t.Fatal("barrier returned before application", err)
	default:
	}
	start()
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(m.ID); !ok || got.Revision != m.Revision {
		t.Fatal("barrier omitted preceding write")
	}
}

func TestBarrierRejectsUnrelatedFieldsAndHonorsCancellation(t *testing.T) {
	if err := validateCommand(Command{Kind: "barrier", At: time.Now(), MonitorID: "hidden-config"}); err == nil {
		t.Fatal("barrier admitted unrelated payload")
	}
	s, start := dormantCatalogStore(t)
	start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled barrier did not stop", err)
	}
}
