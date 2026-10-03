//go:build externaljobs

package worker

import (
	"context"
	"errors"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCheckExpiryAfterJournalRestart(t *testing.T) {
	for _, kind := range []string{"check", "recovery", "notification"} {
		for _, state := range []string{"reserved", "outcome"} {
			for _, explicit := range []bool{false, true} {
				name := kind + "/" + state
				if explicit {
					name += "/explicit"
				} else {
					name += "/generic"
				}
				t.Run(name, func(t *testing.T) {
					cfg := testConfig(t)
					r, err := New(cfg)
					if err != nil {
						t.Fatal(err)
					}
					a := assignment("retired-check", kind)
					if err := r.journal.reserve(a); err != nil {
						t.Fatal(err)
					}
					if state == "outcome" {
						if err := r.journal.update(a.ExecutionID, func(rec *journalRecord) error { rec.State, rec.GrantID = "started", "grant"; return nil }); err != nil {
							t.Fatal(err)
						}
						status := "unknown"
						if kind == "check" {
							status = "noData"
						}
						r.saveOutcome(a.ExecutionID, api.Outcome{Kind: kind, Status: status}, func(err error) { t.Fatal(err) })
					}
					if err := r.Close(); err != nil {
						t.Fatal(err)
					}
					failure := errors.New("record missing or request interrupted")
					if explicit {
						failure = cpra.ErrWorkerCheckExpired
					}
					protocol := &protocolFuncs{fakeProtocol: newFake()}
					calls := 0
					protocol.start = func(_ context.Context, req api.StartRequest) (*api.StartResponse, error) {
						calls++
						if req.Mode != api.StartModeReconcile {
							t.Fatal("restarted a handler")
						}
						return nil, failure
					}
					protocol.result = func(context.Context, api.Outcome) (*api.Receipt, error) { calls++; return nil, failure }
					cfg.Client = protocol
					r, err = New(cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					records, err := r.journal.records()
					if err != nil || len(records) != 1 {
						t.Fatal("journal restart", err)
					}
					err = r.deliver(t.Context(), records[0])
					removed := kind == "check" && explicit
					if (err == nil) != removed || calls != 1 {
						t.Fatal("expiry disposition", err, calls)
					}
					remaining, err := r.journal.records()
					if err != nil || (len(remaining) == 0) != removed {
						t.Fatal("incorrect outbox cleanup", err)
					}
				})
			}
		}
	}
}
