//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWorkerUnknownReceiptRequiresRetainedActionOnNativeImport(t *testing.T) {
	for _, terminal := range []string{"result", "finalization"} {
		for _, corruption := range []string{"intact", "missing", "uid", "revision", "grant", "start", "state"} {
			t.Run(terminal+"/"+corruption, func(t *testing.T) {
				s, authority, _, input := workerResultFixture(t, "recovery", "unknown")
				execution, _, err := s.WorkerExecution(t.Context(), input.ExecutionID)
				if err != nil {
					t.Fatal(err)
				}
				if terminal == "result" {
					if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
						t.Fatal(err)
					}
				} else if _, err := s.FinalizeWorkerExecutions(t.Context(), execution.Intent.Deadline, 1); err != nil {
					t.Fatal(err)
				}
				m, _ := s.Get(execution.Intent.MonitorID)
				if _, live := m.Actions[execution.Intent.ActionID]; live || m.ActionProjection == nil || m.ActionProjection.RecoveryHeld != 1 {
					t.Fatal("fixture did not retire the held action from live execution")
				}
				// A later independent action keeps the ledger's last-command digest
				// valid when an earlier retained row is deliberately omitted below.
				configure(t, s)
				pulse := submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: time.Now().UTC(), Outcome: "failure"})[0]
				if pulse.Err != nil || pulse.Monitor == nil {
					t.Fatal("unrelated action fixture", pulse.Err)
				}
				frozen, err := s.fsm.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				defer frozen.Release()
				snap := frozen.(*frozenSnapshot)
				id := execution.Intent.ActionID
				row, err := decodeActionRow(snap.collections.actions.current[id])
				if err != nil {
					t.Fatal(err)
				}
				original := row
				switch corruption {
				case "missing":
					delete(snap.collections.actions.current, id)
					delete(snap.collections.actions.versions, id)
					m := snap.image.Monitors[row.MonitorID]
					m.ActionProjection.Unknown--
					m.ActionProjection.Held--
					m.ActionProjection.RecoveryHeld--
					m.ActionProjection.LatestRecovery = nil
					snap.image.Monitors[m.ID] = m
				case "uid":
					row.Action.CatalogUID = "different-incarnation"
				case "revision":
					row.Action.Revision = "different-target"
				case "grant":
					row.Action.ExecutorSession = "different-grant"
				case "start":
					row.Action.StartedAt = row.Action.StartedAt.Add(time.Nanosecond)
				case "state":
					row.Action.State = Succeeded
				}
				if corruption != "missing" && corruption != "intact" {
					data, err := encodeActionRow(row)
					if err != nil {
						t.Fatal(err)
					}
					snap.collections.actions.current[id] = data
					versions := snap.collections.actions.versions[id]
					versions[len(versions)-1] = data
				}
				// Keep the export accounting consistent with the damaged fixture so
				// import reaches the worker/action join rather than a byte-count gate.
				if corruption != "intact" {
					if err := snap.collections.actions.accounting.adjust(original, false); err != nil {
						t.Fatal(err)
					}
					if corruption != "missing" {
						if err := snap.collections.actions.accounting.adjust(row, true); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Bypass producer validation to exercise the native import boundary
				// with an internally checksummed but semantically damaged snapshot.
				if snap.image.Version != WorkerDispatchFormatVersion {
					t.Fatal("fixture must encode the current worker snapshot format")
				}
				var raw bytes.Buffer
				raw.WriteString(workerDispatchSnapshotMagic)
				if err := json.NewEncoder(&raw).Encode(snap.image); err != nil {
					t.Fatal(err)
				}
				if err := writeCollectionSnapshotRows(&raw, snap.image.Version, snap.collections); err != nil {
					t.Fatal(err)
				}
				_, ledger, err := decodeSnapshot(bytes.NewReader(raw.Bytes()), filepath.Join(t.TempDir(), "ledger"))
				if ledger != nil {
					defer ledger.Close()
				}
				if corruption == "intact" {
					if err != nil {
						t.Fatal("intact cold action rejected", err)
					}
				} else if !errors.Is(err, ErrWorkerExecutionInvalid) {
					t.Fatal("missing or mismatched unknown action escaped the reverse join", err)
				}
			})
		}
	}
}

func TestWorkerColdReadFailureClosesAdmissionWithoutReaderRace(t *testing.T) {
	s, authority, input := workerEvidenceFixture(t, "recovery")
	// Closing this owned ledger injects a genuine cold lookup failure. No
	// provider is involved, and all concurrent callers use public Store reads.
	s.fsm.mu.Lock()
	err := s.fsm.collections.Close()
	s.fsm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 20 {
				if _, err := s.WorkerEvidenceExecution(t.Context(), authority, input.ServerID, input.ExecutionID, input.OriginalReceiptID); err == nil {
					t.Error("failed cold lookup reported success")
				}
				_ = s.ControllerHealthContext(t.Context())
			}
		})
	}
	group.Wait()
	if s.ControllerHealth() == nil || s.Status().Ready {
		t.Fatal("permanent ledger error was not published to readiness")
	}
	if _, err := s.Submit(t.Context(), []Command{{Kind: "barrier", At: time.Now().UTC()}}); err == nil {
		t.Fatal("new admission bypassed the failed ledger")
	}
}
