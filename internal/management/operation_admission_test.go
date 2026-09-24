package management

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestPreparedSnoozeCannotAdmitAnAlreadyElapsedDeadline(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	m := configureFacadeControl(t, c, store, createResource(t, c, controlResource("service")))
	p, err := c.PrepareControl(ctx, "snooze", m.ID, api.ControlRequest{Revision: m.ControlRevision, Duration: "1ms", Reason: "Short pause"})
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(p.command.Control.Until); remaining > 0 {
		time.Sleep(remaining)
	}
	before := store.Status().CommittedIndex
	if _, err := c.CommitControlAs(ctx, p, "operator"); !errors.Is(err, persistence.ErrControlInvalid) {
		t.Fatal("admission reused preparation time to accept an elapsed deadline", err)
	}
	after, _ := store.Get(m.ID)
	if p.OperationID() != "" || after.ControlRevision != m.ControlRevision || !after.SnoozedUntil.IsZero() || store.Status().CommittedIndex != before {
		t.Fatal("elapsed preflight allocated a handle or changed active controls")
	}
}

func TestPreparedOperationHandleCanBeObservedDuringAdmission(t *testing.T) {
	for _, kind := range []string{"resource", "control", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			c, store := testCatalog(t)
			ctx := context.Background()
			var handle func() string
			var commit func() error
			switch kind {
			case "resource":
				p, err := c.Prepare(ctx, resource("Credential", "private", api.CredentialSpec{Value: api.Pointer("never-returned")}), "", true)
				if err != nil {
					t.Fatal(err)
				}
				handle, commit = p.OperationID, func() error { _, err := c.CommitAs(ctx, p, "operator"); return err }
			case "control":
				m := configureFacadeControl(t, c, store, createResource(t, c, controlResource("service")))
				p, err := c.PrepareControl(ctx, "snooze", m.ID, api.ControlRequest{Revision: m.ControlRevision, Duration: "1m", Reason: "Maintenance"})
				if err != nil {
					t.Fatal(err)
				}
				handle, commit = p.OperationID, func() error { _, err := c.CommitControlAs(ctx, p, "operator"); return err }
			case "recovery":
				m := configureFacadeControl(t, c, store, createResource(t, c, controlResource("service")))
				p, err := c.PrepareAction(ctx, "recover", m.ID, api.ControlRequest{Revision: m.ControlRevision, Reason: "Investigated"})
				if err != nil {
					t.Fatal(err)
				}
				handle, commit = p.OperationID, func() error { _, err := c.CommitActionAs(ctx, p, "operator"); return err }
			}
			if handle() != "" {
				t.Fatal("preparation exposed an unallocated handle")
			}
			started, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				previous := handle()
				close(started)
				for {
					id := handle()
					if id != "" {
						if _, _, err := persistence.ParseOperationHandle(id); err != nil || previous != "" && previous != id {
							done <- fmt.Errorf("observed an incomplete or replaced operation handle")
							return
						}
						previous = id
					} else if previous != "" {
						done <- fmt.Errorf("confirmed handle disappeared during admission")
						return
					}
					select {
					case <-stop:
						done <- nil
						return
					default:
						runtime.Gosched()
					}
				}
			}()
			<-started
			err := commit()
			close(stop)
			if observationErr := <-done; observationErr != nil {
				t.Fatal(observationErr)
			}
			if kind == "recovery" {
				if !errors.Is(err, persistence.ErrRecoveryIneligible) {
					t.Fatal("fixture recovery was not explicitly rejected as ineligible", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			// This fixture has no recovery driver, so its action is rejected after
			// allocation. Both accepted and rejected targets retain a readable ID.
			if _, _, err := persistence.ParseOperationHandle(handle()); err != nil {
				t.Fatal("admission lost its confirmed operation handle", err)
			}
			if _, err := c.Operation(ctx, handle()); err != nil {
				t.Fatal("observed handle has no authoritative receipt", err)
			}
		})
	}
}
