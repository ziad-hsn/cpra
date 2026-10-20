package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestActionReaderRetainHasIndependentCloseOwnership(t *testing.T) {
	l := newLedgerTest(t, true, 1<<20)
	if _, err := l.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
		t.Fatal(err)
	}
	pin, err := l.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	view := ActionView{reader: newActionReader(pin)}
	retained, err := view.Retain()
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := view.Get("a"); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("closed handle remained readable", err)
	}
	if _, found, err := retained.Get("a"); err != nil || !found {
		t.Fatal("closing owner invalidated retained reader", err)
	}
	if err := retained.Close(); err != nil || l.viewPins.Load() != 0 {
		t.Fatal("last reader did not release generation", err)
	}
	if _, err := retained.Retain(); !errors.Is(err, errCollectionLedgerClosed) {
		t.Fatal("closed handle was retained", err)
	}
}

func TestActionReaderCancelsWhileLedgerWriterIsBusy(t *testing.T) {
	for _, page := range []bool{false, true} {
		l := newLedgerTest(t, true, 1<<20)
		if _, err := l.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
			t.Fatal(err)
		}
		pin, err := l.pinActions()
		if err != nil {
			t.Fatal(err)
		}
		view := ActionView{reader: newActionReader(pin)}
		l.mu.Lock()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			// Match request cleanup: cancellation is only useful if releasing
			// the final reader also returns before the writer completes.
			var result error
			defer func() { done <- errors.Join(result, view.Close()) }()
			if page {
				_, _, err := view.PageContext(ctx, "", "", 100)
				result = err
				return
			}
			_, found, err := view.GetContext(ctx, "a")
			if found {
				result = errors.New("cancelled read returned evidence")
				return
			}
			result = err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Error("read did not respect cancellation", err)
			}
		case <-time.After(time.Second):
			t.Error("read waited for writer after cancellation")
		}
		l.mu.Unlock()
		cancel()
		_ = view.Close()
	}
}

func TestActionPinCreationCancelsWhileLedgerWriterIsBusy(t *testing.T) {
	l := newLedgerTest(t, true, 1<<20)
	if _, err := l.CommitActions(actionLedgerPosition{Index: 1}, []actionLedgerRow{testActionRow("a")}); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		pin, err := l.pinActionsContext(ctx)
		if pin != nil {
			_ = pin.Close()
			done <- errors.New("cancelled pin creation returned a reader")
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("pin creation ignored cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pin creation waited for writer after cancellation")
	}
	if l.viewPins.Load() != 0 {
		t.Fatal("cancelled pin creation retained the generation")
	}
}
