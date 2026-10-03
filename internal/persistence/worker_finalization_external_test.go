//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerFinalizationExpiresEveryAdmissionPhase(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		for _, phase := range []string{"queued", "offered", "started"} {
			t.Run(category+"/"+phase, func(t *testing.T) {
				s, a, pollRequest, in, _ := workerOfferFixture(t, category)
				var start WorkerStartRequest
				if phase != "queued" {
					poll, err := s.CommitWorkerPoll(t.Context(), a, pollRequest)
					if err != nil {
						t.Fatal(err)
					}
					start = workerStartFromOffer(poll)
					if phase == "started" {
						if _, err := s.CommitWorkerStart(t.Context(), a, start); err != nil {
							t.Fatal(err)
						}
					}
				}
				before, _ := s.Get(in.MonitorID)
				used := s.fsm.image.WorkerExecutions.EncodedBytes
				if count, err := s.FinalizeWorkerExecutions(t.Context(), in.Deadline.Add(-time.Nanosecond), 1); err != nil || count != 0 {
					t.Fatal("early expiry", count, err)
				}
				if count, err := s.FinalizeWorkerExecutions(t.Context(), in.Deadline, 1); err != nil || count != 1 {
					t.Fatal("expiry", count, err)
				}
				r, _, _ := s.WorkerExecution(t.Context(), in.ID)
				m, _ := s.Get(in.MonitorID)
				if r.Finalization == nil || r.Result != nil || r.Finalization.Reason != "deadline" || r.Finalization.NoData != (category == "check") || r.Finalization.TimedOut != (category == "check") {
					t.Fatal("invented worker result or lost missing/timeout classification")
				}
				if category == "check" {
					if m.Generation != in.Generation || m.NextCheck.Before(in.Scheduled.Add(m.Policy.Interval)) {
						t.Fatal("missing generation not finalized")
					}
					comparison := m.Clone()
					comparison.Generation, comparison.NextCheck = before.Generation, before.NextCheck
					if !reflect.DeepEqual(comparison, before) {
						t.Fatal("missing executor changed health, incident, counters or verification")
					}
				} else {
					want, disposition := Cancelled, "finalized"
					if phase == "started" {
						want, disposition = Unknown, "unknown"
					}
					if m.Actions[in.ActionID].State != want || r.Finalization.Receipt.Disposition != disposition || m.InterventionAttempts != before.InterventionAttempts {
						t.Fatal("unstarted/unknown action distinction lost")
					}
				}
				if s.fsm.image.WorkerExecutions.EncodedBytes != used {
					t.Fatal("finalization consumed unreserved intent capacity")
				}
				if _, err := s.FinalizeWorkerExecutions(t.Context(), in.Deadline.Add(time.Second), 1); err != nil {
					t.Fatal(err)
				}
				again, _, _ := s.WorkerExecution(t.Context(), in.ID)
				if !reflect.DeepEqual(r, again) {
					t.Fatal("duplicate finalization changed receipt")
				}
				if phase != "queued" {
					result := workerStartAt(t, s, a, start, in.Deadline.Add(time.Second))
					if result.Err != nil || result.WorkerStart == nil || result.WorkerStart.Disposition == "granted" || result.WorkerStart.ReceiptID != r.Finalization.Receipt.ReceiptID {
						t.Fatal("terminal start", result.Err)
					}
				}
				if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
					t.Fatal("snapshot", err)
				}
			})
		}
	}
}

func TestWorkerFinalizationOwnerFenceAndLateEvidence(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, start, input := workerResultFixture(t, category, map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category])
			r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			at := time.Now().UTC()
			owner := uuid.NewString()
			if result := submit(t, s, Command{Kind: "local_session", At: at, ExecutorSession: owner})[0]; result.Err != nil || !result.Allowed {
				t.Fatal(result.Err)
			}
			command := WorkerFinalizeCommand{ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, OwnerEpoch: s.executorSession}
			if result := submit(t, s, Command{Kind: "worker_finalize", At: at, commandExtensions: commandExtensions{WorkerFinalize: &command}})[0]; !errors.Is(result.Err, ErrWorkerSessionExpired) {
				t.Fatal("old owner finalized work", result.Err)
			}
			command.OwnerEpoch = owner
			mustResult(t, s, Command{Kind: "worker_finalize", At: at, commandExtensions: commandExtensions{WorkerFinalize: &command}})
			r, _, _ = s.WorkerExecution(t.Context(), r.Intent.ID)
			if r.Finalization == nil || r.Finalization.Reason != "owner_lost" || r.Finalization.TimedOut {
				t.Fatal("owner loss was fabricated as timeout")
			}
			// Restore the Store object's actual owner; that cannot revive work sealed
			// by the intervening owner. All subsequent public methods use real time.
			if result := submit(t, s, Command{Kind: "local_session", At: time.Now().UTC(), ExecutorSession: s.executorSession})[0]; result.Err != nil || !result.Allowed {
				t.Fatal(result.Err)
			}
			receipt, err := s.CommitWorkerResult(t.Context(), a, input)
			if err != nil || receipt != r.Finalization.Receipt {
				t.Fatal("late result replaced finalization", err)
			}
			current, _, _ := s.WorkerExecution(t.Context(), r.Intent.ID)
			if !reflect.DeepEqual(r, current) {
				t.Fatal("late result created worker outcome")
			}
			before, _ := s.Get(r.Intent.MonitorID)
			if category != "check" {
				mustResult(t, s, reviewCommand(before, before.Actions[r.Intent.ActionID], uuid.NewString(), "inconclusive", time.Now().UTC()))
				before, _ = s.Get(before.ID)
			}
			evidence := WorkerEvidenceInput{ServerID: input.ServerID, ExecutionID: input.ExecutionID, ExecutionRevision: input.ExecutionRevision, GrantID: input.GrantID,
				OriginalReceiptID: receipt.ReceiptID, EvidenceID: uuid.NewString(), Digest: identity("late evidence after expiry"), Payload: input.Payload.Clone()}
			first, err := s.CommitWorkerEvidence(t.Context(), a, evidence)
			if err != nil {
				t.Fatal(err)
			}
			repeat, err := s.CommitWorkerEvidence(t.Context(), a, evidence)
			if err != nil || first != repeat {
				t.Fatal("late evidence duplicated", err)
			}
			after, _ := s.Get(before.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("evidence changed health or operator review")
			}
			start.Mode = "reconcile"
			grant, err := s.CommitWorkerStart(t.Context(), a, start)
			if err != nil || grant.ReceiptID != receipt.ReceiptID || grant.Disposition == "granted" {
				t.Fatal("finalized grant revived", err)
			}
			if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerFinalizationRacesResultWithoutReplacingWinner(t *testing.T) {
	for n := 0; n < 8; n++ {
		s, a, _, input := workerResultFixture(t, "check", "success")
		original, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
		var wg sync.WaitGroup
		var receipt WorkerReceipt
		var resultErr, expiryErr error
		wg.Go(func() { receipt, resultErr = s.CommitWorkerResult(t.Context(), a, input) })
		wg.Go(func() { _, expiryErr = s.FinalizeWorkerExecutions(t.Context(), original.Intent.Deadline, 1) })
		wg.Wait()
		if resultErr != nil || expiryErr != nil {
			t.Fatal(resultErr, expiryErr)
		}
		r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
		winner, terminal := r.TerminalReceipt()
		if !terminal || winner != receipt || (r.Result == nil) == (r.Finalization == nil) {
			t.Fatal("both result and finalization won")
		}
		if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerFinalizationSnapshotAndCommandValidation(t *testing.T) {
	s, _, _, input := workerResultFixture(t, "check", "success")
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	c := WorkerFinalizeCommand{ExecutionID: r.Intent.ID, ExecutionRevision: r.Intent.Revision, OwnerEpoch: s.executorSession}
	command := Command{Kind: "worker_finalize", At: r.Intent.Deadline, commandExtensions: commandExtensions{WorkerFinalize: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerFinalizationFormatVersion || version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion) {
			t.Fatal("format", version, err)
		}
	}
	malformed := command
	malformed.MonitorID = r.Intent.MonitorID
	if err := validateCommand(malformed); err == nil {
		t.Fatal("extra command field accepted")
	}
	mustResult(t, s, command)
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	frozen, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for name, corrupt := range map[string]func(*WorkerFinalization){
		"receipt": func(v *WorkerFinalization) { v.Receipt.ReceiptID = uuid.NewString() },
		"worker":  func(v *WorkerFinalization) { v.Receipt.WorkerUID = uuid.NewString() },
		"reason":  func(v *WorkerFinalization) { v.Reason = "completed" },
		"early":   func(v *WorkerFinalization) { v.RecordedAt = r.CreatedAt },
		"index":   func(v *WorkerFinalization) { v.CommittedIndex++ },
		"no-data": func(v *WorkerFinalization) { v.NoData = false },
		"timeout": func(v *WorkerFinalization) { v.TimedOut = false },
	} {
		t.Run(name, func(t *testing.T) {
			i := frozen
			i.imageExtensions = cloneImageExtensions(frozen)
			row := i.WorkerExecutions.Records[r.Intent.ID]
			corrupt(row.Finalization)
			i.WorkerExecutions.Records[r.Intent.ID] = row
			if validateWorkerExecutionImage(i) == nil {
				t.Fatal("corrupt finalization accepted")
			}
		})
	}
	if validateWorkerExecutionImage(frozen) != nil {
		t.Fatal("snapshot clone was mutable")
	}
	raw, _ := json.Marshal(frozen)
	if bytes.Contains(raw, []byte("private-worker-result-canary")) {
		t.Fatal("finalization persisted fake plaintext worker result")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.FinalizeWorkerExecutions(ctx, time.Now(), 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
}

func TestWorkerFinalizationBoundedWorkAndGuards(t *testing.T) {
	s := openCatalogMemory(t)
	var executions []WorkerExecutionIntent
	for n := 0; n < 7; n++ {
		in, _ := workerExecutionFixture(t, s, "check")
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		executions = append(executions, in)
	}
	at := time.Now().UTC().Add(2 * time.Hour)
	for _, want := range []int{2, 2, 2, 1, 0} {
		count, err := s.FinalizeWorkerExecutions(t.Context(), at, 2)
		if err != nil || count != want {
			t.Fatal("bounded finalization", count, want, err)
		}
	}
	for _, in := range executions {
		r, _, _ := s.WorkerExecution(t.Context(), in.ID)
		if r.Finalization == nil || r.Finalization.Receipt.WorkerUID != "" || r.Finalization.Receipt.ServerID != "" || r.Lifecycle != nil {
			t.Fatal("unassigned execution acquired a fabricated worker identity")
		}
	}
	if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal(err)
	}
	c := WorkerFinalizeCommand{ExecutionID: executions[0].ID, ExecutionRevision: uuid.NewString(), OwnerEpoch: s.executorSession}
	result := submit(t, s, Command{Kind: "worker_finalize", At: at, commandExtensions: commandExtensions{WorkerFinalize: &c}})[0]
	if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
		t.Fatal("different execution revision accepted")
	}
	for _, limit := range []int{0, MaxWorkerFinalizationsPerTick + 1} {
		if _, err := s.FinalizeWorkerExecutions(t.Context(), at, limit); !errors.Is(err, ErrWorkerExecutionInvalid) {
			t.Fatal("invalid limit accepted", limit, err)
		}
	}
	if _, err := s.FinalizeWorkerExecutions(nil, at, 1); !errors.Is(err, ErrWorkerExecutionInvalid) {
		t.Fatal("nil context accepted", err)
	}
}

func TestWorkerFinalizationReceiptSeparatesValidIdentityFields(t *testing.T) {
	first := WorkerExecutionRecord{Intent: WorkerExecutionIntent{ID: "a/b", Revision: "c", Category: "check"}}
	second := WorkerExecutionRecord{Intent: WorkerExecutionIntent{ID: "a", Revision: "b/c", Category: "check"}}
	for _, record := range []WorkerExecutionRecord{first, second} {
		if !catalogIdentifier(record.Intent.ID, 256) || !catalogIdentifier(record.Intent.Revision, 256) {
			t.Fatal("fixture must exercise valid persisted identifiers")
		}
	}
	if workerFinalizationReceipt(first, 42).ReceiptID == workerFinalizationReceipt(second, 42).ReceiptID {
		t.Fatal("different execution identities in one committed batch share a receipt")
	}
	if workerFinalizationReceipt(first, 42) != workerFinalizationReceipt(first, 42) {
		t.Fatal("receipt identity is not deterministic")
	}
}
