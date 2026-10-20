//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerResultFixture(t *testing.T, category, status string) (*Store, WorkerAuthority, WorkerStartRequest, WorkerResultInput) {
	t.Helper()
	s, a, request, in, _ := workerOfferFixture(t, category)
	poll, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	startRequest := workerStartFromOffer(poll)
	start, err := s.CommitWorkerStart(t.Context(), a, startRequest)
	if err != nil || start.Disposition != "granted" {
		t.Fatal("grant", err)
	}
	payload, err := catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution", ID: in.ID, UID: in.MonitorUID, Revision: in.Revision, Purpose: "worker-outcome-test"}, []byte("private-worker-result-canary"))
	if err != nil {
		t.Fatal(err)
	}
	input := WorkerResultInput{ServerID: start.ServerID, ExecutionID: in.ID, ExecutionRevision: in.Revision, GrantID: start.GrantID,
		Status: status, Retryable: status == "rejected", Digest: identity("first-outcome"), Payload: payload}
	return s, a, startRequest, input
}

func TestWorkerResultOriginalReceiptAllCategories(t *testing.T) {
	for category, statuses := range map[string][]string{
		"check": {"success", "failure", "noData"}, "recovery": {"accepted", "completed", "rejected", "unknown"}, "notification": {"accepted", "delivered", "rejected", "unknown"},
	} {
		for _, status := range statuses {
			t.Run(category+"/"+status, func(t *testing.T) {
				s, a, start, input := workerResultFixture(t, category, status)
				original, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				before, _ := monitorWithActionHistory(t, s, original.Intent.MonitorID)
				receipt, err := s.CommitWorkerResult(t.Context(), a, input)
				if err != nil || receipt.ExecutionID != input.ExecutionID || receipt.WorkerUID != a.WorkerUID || receipt.ReceiptID == "" {
					t.Fatal("result", receipt, err)
				}
				want := "accepted"
				if status == "unknown" {
					want = "unknown"
				}
				if status == "noData" {
					want = "finalized"
				}
				if receipt.Disposition != want {
					t.Fatal("disposition", receipt.Disposition, want)
				}
				saved, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				if saved.Result == nil || saved.Result.Receipt != receipt || saved.Result.Input.Status != status {
					t.Fatal("receipt not retained")
				}
				after, _ := monitorWithActionHistory(t, s, original.Intent.MonitorID)
				if category == "check" {
					if after.Generation != original.Intent.Generation {
						t.Fatal("check watermark not finalized")
					}
					if status == "noData" {
						if after.TotalChecks != before.TotalChecks || after.SuccessfulChecks != before.SuccessfulChecks || after.PulseFailures != before.PulseFailures || after.LastOutcome != before.LastOutcome || !after.LastCheck.Equal(before.LastCheck) {
							t.Fatal("missing data changed observed health")
						}
					} else if after.TotalChecks != before.TotalChecks+1 || after.LastOutcome != status || after.LatencyAvailable {
						t.Fatal("observation missing or fabricated remote execution timing")
					}
				} else if status == "unknown" {
					if after.Actions[original.Intent.ActionID].State != Unknown {
						t.Fatal("ambiguous action not held")
					}
				} else if status != "rejected" && after.Actions[original.Intent.ActionID].Outcome != status {
					t.Fatal("provider acceptance conflated with completed/delivered")
				}
				duplicate := input
				duplicate.Payload = input.Payload.Clone()
				duplicate.Payload.Ciphertext[0] ^= 1 // A retry may be resealed; the first envelope stays authoritative.
				replayed, err := s.CommitWorkerResult(t.Context(), a, duplicate)
				if err != nil || replayed != receipt {
					t.Fatal("receipt replay", err)
				}
				retained, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				unchanged, _ := monitorWithActionHistory(t, s, original.Intent.MonitorID)
				if !reflect.DeepEqual(saved, retained) || !reflect.DeepEqual(after, unchanged) {
					t.Fatal("duplicate changed committed state")
				}
				input.Digest = identity("conflicting-outcome")
				if _, err = s.CommitWorkerResult(t.Context(), a, input); !errors.Is(err, ErrWorkerExecutionConflict) {
					t.Fatal("conflict", err)
				}
				for _, mode := range []string{"begin", "reconcile"} {
					start.Mode = mode
					result, err := s.CommitWorkerStart(t.Context(), a, start)
					if err != nil || result.Disposition != "terminal" || result.ReceiptID != receipt.ReceiptID || result.GrantID != "" || !result.Deadline.IsZero() {
						t.Fatal("terminal Start regranted or lost receipt", result, err)
					}
				}
				if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
					t.Fatal("execution image", err)
				}
				if err := validateRecoveryReviewImage(s.fsm.image); err != nil {
					t.Fatal("action image", err)
				}
				if err := validateWorkerSessionImage(s.fsm.image); err != nil {
					t.Fatal("session image", err)
				}
			})
		}
	}
}

func TestWorkerRecoveryRejectedRetryPreservesBudgetAfterSnapshot(t *testing.T) {
	s, authority, _, input := workerResultFixture(t, "recovery", "rejected")
	execution, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	m, _ := monitorWithActionHistory(t, s, execution.Intent.MonitorID)
	first := m.Actions[execution.Intent.ActionID]
	config := m.Clone()
	config.Policy.RecoveryMaxAttempts = 2
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: &execution.Intent.Guard, At: time.Now().UTC()})
	if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
		t.Fatal(err)
	}
	m, _ = monitorWithActionHistory(t, s, m.ID)
	if a := m.Actions[first.ID]; a.State != Failed || !a.Retryable || !a.Deadline.Equal(first.Deadline) {
		t.Fatal("worker rejection lost its positive retry evidence or deadline")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	m, _ = monitorWithActionHistory(t, s, m.ID)
	r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: time.Now().UTC(), Outcome: "failure", Guard: &execution.Intent.Guard})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	next := interventionAction(t, *r.Monitor)
	if next.State != Queued || next.Attempt != 2 || next.ID == first.ID || !next.QueuedAt.Equal(first.QueuedAt) || !next.Deadline.Equal(first.Deadline) {
		t.Fatal("restored worker rejection did not continue within the original execution budget")
	}
}

func TestWorkerResultConcurrentDeliveryAndOwnership(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "recovery", "completed")
	for name, change := range map[string]func(*WorkerAuthority, *WorkerResultInput){
		"worker":     func(a *WorkerAuthority, _ *WorkerResultInput) { a.WorkerUID = uuid.NewString() },
		"credential": func(a *WorkerAuthority, _ *WorkerResultInput) { a.CredentialRevision = uuid.NewString() },
		"epoch":      func(a *WorkerAuthority, _ *WorkerResultInput) { a.Epoch = uuid.NewString() },
		"grant":      func(_ *WorkerAuthority, in *WorkerResultInput) { in.GrantID = uuid.NewString() },
		"execution":  func(_ *WorkerAuthority, in *WorkerResultInput) { in.ExecutionID = uuid.NewString() },
		"revision":   func(_ *WorkerAuthority, in *WorkerResultInput) { in.ExecutionRevision = uuid.NewString() },
		"server":     func(_ *WorkerAuthority, in *WorkerResultInput) { in.ServerID = uuid.NewString() },
		"category":   func(_ *WorkerAuthority, in *WorkerResultInput) { in.Status = "success" },
	} {
		t.Run(name, func(t *testing.T) {
			badAuthority, badInput := a, input
			change(&badAuthority, &badInput)
			if _, err := s.CommitWorkerResult(t.Context(), badAuthority, badInput); err == nil {
				t.Fatal("foreign result accepted")
			}
		})
	}
	var wg sync.WaitGroup
	receipts, failures := make(chan WorkerReceipt, 16), make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.CommitWorkerResult(t.Context(), a, input)
			receipts <- r
			failures <- err
		}()
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := <-receipts
	for r := range receipts {
		if r != first {
			t.Fatal("duplicate receipts differ")
		}
	}
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	m, _ := monitorWithActionHistory(t, s, r.Intent.MonitorID)
	if m.Actions[r.Intent.ActionID].State != Succeeded || m.VerifyRemaining != max(1, m.Policy.Healthy) {
		t.Fatal("result transition not applied")
	}
}

func TestWorkerResultMissingAndDelayedCheckPreserveHealth(t *testing.T) {
	for _, category := range []string{"check", "recovery"} {
		t.Run(category, func(t *testing.T) {
			status := "completed"
			if category == "check" {
				status = "success"
			}
			s, a, _, input := workerResultFixture(t, category, status)
			r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			before, _ := monitorWithActionHistory(t, s, r.Intent.MonitorID)
			command := WorkerResultCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: input}
			result := submit(t, s, Command{Kind: "worker_result", At: r.Intent.Deadline, commandExtensions: commandExtensions{WorkerResult: &command}})[0]
			if result.Err != nil || result.WorkerResult == nil {
				t.Fatal(result.Err)
			}
			after, _ := monitorWithActionHistory(t, s, r.Intent.MonitorID)
			if category == "check" {
				if result.WorkerResult.Disposition != "finalized" || after.LastOutcome != before.LastOutcome || after.TotalChecks != before.TotalChecks || after.Generation != r.Intent.Generation {
					t.Fatal("late check invented health")
				}
			} else if result.WorkerResult.Disposition != "unknown" || after.Actions[r.Intent.ActionID].State != Unknown {
				t.Fatal("late action changed held policy")
			}
		})
	}
}

func TestWorkerResultReservedCapacityAndSnapshotIsolation(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "notification", "delivered")
	s.fsm.mu.Lock()
	state := s.fsm.image.WorkerExecutions
	originalBytes := state.EncodedBytes
	state.EncodedBytes = MaxWorkerExecutionBytes
	s.fsm.mu.Unlock()
	receipt, err := s.CommitWorkerResult(t.Context(), a, input)
	if err != nil || receipt.ReceiptID == "" {
		t.Fatal("full intent namespace blocked its reserved outcome", err)
	}
	s.fsm.mu.Lock()
	used := s.fsm.image.WorkerExecutions.EncodedBytes
	s.fsm.image.WorkerExecutions.EncodedBytes = originalBytes
	s.fsm.mu.Unlock()
	if used != MaxWorkerExecutionBytes {
		t.Fatal("outcome consumed intent capacity")
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	// Public reads must not expose mutable ciphertext or receipt fields.
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	r.Result.Input.Payload.Ciphertext[0] ^= 1
	r.Result.Receipt.ReceiptID = "changed"
	sink := &collectionTestSink{}
	if err = snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(actionWorkerSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("private-worker-result-canary")) {
		t.Fatal("format or plaintext leak")
	}
	decoded, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if decoded.WorkerExecutions.Records[input.ExecutionID].Result.Receipt != receipt {
		t.Fatal("receipt not frozen")
	}
	for _, change := range []func(*image){
		func(i *image) { i.Version = WorkerOfferFormatVersion },
		func(i *image) {
			v := i.WorkerExecutions.Records[input.ExecutionID]
			v.Result.Input.GrantID = "wrong"
			i.WorkerExecutions.Records[input.ExecutionID] = v
		},
		func(i *image) {
			v := i.WorkerExecutions.Records[input.ExecutionID]
			v.Result.Receipt.WorkerUID = "wrong"
			i.WorkerExecutions.Records[input.ExecutionID] = v
		},
		func(i *image) {
			v := i.WorkerExecutions.Records[input.ExecutionID]
			v.Result.Receipt.ReceiptID = "another-valid-looking-receipt"
			i.WorkerExecutions.Records[input.ExecutionID] = v
		},
		func(i *image) {
			v := i.WorkerExecutions.Records[input.ExecutionID]
			v.Result.CommittedIndex = i.Index + 1
			i.WorkerExecutions.Records[input.ExecutionID] = v
		},
	} {
		raw, _ := json.Marshal(decoded)
		var copy image
		if err := json.Unmarshal(raw, &copy); err != nil {
			t.Fatal(err)
		}
		change(&copy)
		if err := validateWorkerExecutionImage(copy); err == nil {
			t.Fatal("corrupt receipt accepted")
		}
	}
}

func TestWorkerResultCommandBoundsAndFormat(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "check", "success")
	c := Command{Kind: "worker_result", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerResult: &WorkerResultCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: input}}}
	if commandMinimumFormat(c) != WorkerResultFormatVersion {
		t.Fatal("result format not gated")
	}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(raw)
		if (err == nil) != (version == WorkerResultFormatVersion || version == WorkerLifecycleFormatVersion || version == WorkerFinalizationFormatVersion || version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion) {
			t.Fatal("result format gate", version, err)
		}
	}
	for name, change := range map[string]func(*Command){
		"mixed":      func(c *Command) { c.MonitorID = "unrelated" },
		"kind":       func(c *Command) { c.Kind = "worker_start" },
		"missing":    func(c *Command) { c.WorkerResult = nil },
		"digest":     func(c *Command) { c.WorkerResult.Input.Digest = "bad" },
		"identity":   func(c *Command) { c.WorkerResult.Input.ExecutionID = strings.Repeat("x", 257) },
		"ciphertext": func(c *Command) { c.WorkerResult.Input.Payload.Ciphertext = make([]byte, MaxWorkerExecutionPayload+17) },
	} {
		t.Run(name, func(t *testing.T) {
			copy := c
			value := *c.WorkerResult
			copy.WorkerResult = &value
			change(&copy)
			if validateCommand(copy) == nil {
				t.Fatal("invalid command accepted", fmt.Sprint(name))
			}
		})
	}
}

func TestWorkerResultLargeOutcomesRemainPaged(t *testing.T) {
	s := openWorkerMemory(t)
	first, job := workerExecutionFixture(t, s, "check")
	worker := workerPrincipal("large-result-worker")
	worker.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID,
		Version: job.Current.Version, Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"*"}}}
	s.administrative = true
	policy := commitWorker(t, s, workerCommand(t, s, "bootstrap", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	authority, err := s.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 24; n++ {
		in := first
		if n > 0 {
			in = workerExecutionFixtureType(t, s, job)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	server, err := s.WorkerProtocolIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.CommitWorkerPoll(t.Context(), authority, WorkerPollRequest{ServerID: server.ServerID,
		WorkerID: worker.ID, ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 24, Limit: 24,
		Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: "check"}}})
	if err != nil || len(response.Offers) != 24 {
		t.Fatal("large result offers", err)
	}
	for _, offer := range response.Offers {
		start, err := s.CommitWorkerStart(t.Context(), authority, WorkerStartRequest{ServerID: server.ServerID,
			SessionID: response.SessionID, ExecutionID: offer.ExecutionID, ExecutionRevision: offer.ExecutionRevision, LeaseID: offer.LeaseID, Mode: "begin"})
		if err != nil || start.Disposition != "granted" {
			t.Fatal("large result grant", err)
		}
		payload, err := catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution",
			ID: offer.ExecutionID, UID: worker.UID, Revision: offer.ExecutionRevision, Purpose: "worker-large-outcome-test"}, bytes.Repeat([]byte{31}, MaxWorkerExecutionPayload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitWorkerResult(t.Context(), authority, WorkerResultInput{ServerID: server.ServerID, ExecutionID: offer.ExecutionID,
			ExecutionRevision: offer.ExecutionRevision, GrantID: start.GrantID, Status: "success", Digest: identity("large-result"), Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	seen, after, pages := map[string]bool{}, "", 0
	for {
		page, err := s.WorkerExecutions(t.Context(), after, 100)
		if err != nil || len(page.Items) == 0 {
			t.Fatal("result traversal", err)
		}
		pages++
		raw, err := json.Marshal(page)
		if err != nil || len(raw) > MaxWorkerExecutionPageBytes {
			t.Fatal("result page exceeded byte bound", len(raw), err)
		}
		for _, record := range page.Items {
			if seen[record.Intent.ID] || record.Intent.ID <= after || record.Result == nil {
				t.Fatal("result traversal duplicated or omitted an outcome")
			}
			seen[record.Intent.ID] = true
		}
		if !page.More {
			break
		}
		if page.Next <= after {
			t.Fatal("cursor made no progress")
		}
		after = page.Next
	}
	if len(seen) != 24 || pages < 2 {
		t.Fatal("outcomes not bounded across pages", len(seen), pages)
	}
}

func TestWorkerResultCurrentCredentialWithoutNewWorkGrant(t *testing.T) {
	s, authority, _, input := workerResultFixture(t, "recovery", "completed")
	policy, err := s.WorkerPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	worker := policy.Workers[authority.WorkerID]
	worker.Grants, worker.GrantRevision = nil, uuid.NewString()
	worker.TokenSHA256, worker.CredentialRevision = authenticationVerifier("current-result-worker"), uuid.NewString()
	s.administrative = true
	policy = commitWorker(t, s, workerCommand(t, s, "upsert", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err := s.CommitWorkerResult(t.Context(), authority, input); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("old credential delivered a result", err)
	}
	fresh, err := s.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CommitWorkerResult(t.Context(), fresh, input)
	if err != nil || first.Disposition != "accepted" {
		t.Fatal("current credential could not deliver its original result", err)
	}
	worker.Revoked = true
	worker.CredentialRevision = uuid.NewString()
	s.administrative = true
	policy = commitWorker(t, s, workerCommand(t, s, "upsert", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err := s.CommitWorkerResult(t.Context(), fresh, input); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("revoked principal read retained receipt", err)
	}
}
