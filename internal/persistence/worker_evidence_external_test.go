//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func workerEvidenceFixture(t *testing.T, category string) (*Store, WorkerAuthority, WorkerEvidenceInput) {
	t.Helper()
	status := "unknown"
	if category == "check" {
		status = "noData"
	}
	s, a, _, in := workerResultFixture(t, category, status)
	receipt, err := s.CommitWorkerResult(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, WorkerEvidenceInput{ServerID: in.ServerID, ExecutionID: in.ExecutionID, ExecutionRevision: in.ExecutionRevision,
		GrantID: in.GrantID, OriginalReceiptID: receipt.ReceiptID, EvidenceID: uuid.NewString(), Digest: identity("first-evidence"), Payload: in.Payload.Clone()}
}

func TestWorkerEvidencePreservesOutcomeAndOperatorReview(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, in := workerEvidenceFixture(t, category)
			original, _, _ := s.WorkerExecution(t.Context(), in.ExecutionID)
			m, _ := s.Get(original.Intent.MonitorID)
			if category != "check" {
				mustResult(t, s, reviewCommand(m, retainedAction(t, s, original.Intent.ActionID), uuid.NewString(), "inconclusive", time.Now().UTC()))
				m, _ = s.Get(m.ID)
			}
			first, err := s.CommitWorkerEvidence(t.Context(), a, in)
			if err != nil || first.Disposition != "accepted" || first.ReceiptID == in.OriginalReceiptID {
				t.Fatal("evidence receipt", first, err)
			}
			before, _, _ := s.WorkerExecution(t.Context(), in.ExecutionID)
			// Identical retries can be resealed; keep the first original ciphertext.
			retry := in
			retry.Payload = in.Payload.Clone()
			retry.Payload.Ciphertext[0] ^= 1
			again, err := s.CommitWorkerEvidence(t.Context(), a, retry)
			if err != nil || again != first {
				t.Fatal("evidence replay", err)
			}
			after, _, _ := s.WorkerExecution(t.Context(), in.ExecutionID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("duplicate changed evidence")
			}
			in.Digest = identity("contradictory-evidence")
			if _, err := s.CommitWorkerEvidence(t.Context(), a, in); !errors.Is(err, ErrWorkerExecutionConflict) {
				t.Fatal("evidence identity reused", err)
			}
			in.EvidenceID = uuid.NewString()
			second, err := s.CommitWorkerEvidence(t.Context(), a, in)
			if err != nil || second.ReceiptID == first.ReceiptID {
				t.Fatal("separate evidence not appended", err)
			}
			after, _, _ = s.WorkerExecution(t.Context(), in.ExecutionID)
			current, _ := s.Get(m.ID)
			if len(after.Evidence) != 2 || !reflect.DeepEqual(original.Result, after.Result) || !reflect.DeepEqual(original.Lifecycle, after.Lifecycle) || !reflect.DeepEqual(m, current) {
				t.Fatal("supplemental evidence replaced outcome, review, or health")
			}
			if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
				t.Fatal(err)
			}
			if err := validateRecoveryReviewImage(s.fsm.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerEvidenceConcurrentReplayAndCapacity(t *testing.T) {
	s, a, in := workerEvidenceFixture(t, "recovery")
	var wg sync.WaitGroup
	replies := make(chan WorkerReceipt, 12)
	errorsFound := make(chan error, 12)
	for n := 0; n < 12; n++ {
		wg.Go(func() { v, err := s.CommitWorkerEvidence(t.Context(), a, in); replies <- v; errorsFound <- err })
	}
	wg.Wait()
	close(replies)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first WorkerReceipt
	for v := range replies {
		if first.ReceiptID == "" {
			first = v
		}
		if v != first {
			t.Fatal("concurrent evidence duplicated")
		}
	}
	for n := 1; n < MaxWorkerEvidencePerExecution; n++ {
		next := in
		next.EvidenceID = uuid.NewString()
		if _, err := s.CommitWorkerEvidence(t.Context(), a, next); err != nil {
			t.Fatal(err)
		}
	}
	next := in
	next.EvidenceID = uuid.NewString()
	if _, err := s.CommitWorkerEvidence(t.Context(), a, next); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("evidence limit not enforced", err)
	}
	if v, err := s.CommitWorkerEvidence(t.Context(), a, in); err != nil || v != first {
		t.Fatal("full evidence list blocked receipt replay", err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), in.ExecutionID)
	if len(r.Evidence) != MaxWorkerEvidencePerExecution {
		t.Fatal("quota changed retained evidence")
	}
	// Evidence quota must never block original outcome replay.
	if v, err := s.CommitWorkerResult(t.Context(), a, r.Result.Input); err != nil || v != r.Result.Receipt {
		t.Fatal("evidence blocked original result", err)
	}
	if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerEvidenceRejectsForeignReceiptsAndKnownResults(t *testing.T) {
	s, a, in := workerEvidenceFixture(t, "recovery")
	for _, change := range []func(*WorkerAuthority, *WorkerEvidenceInput){
		func(a *WorkerAuthority, _ *WorkerEvidenceInput) { a.WorkerUID = uuid.NewString() },
		func(a *WorkerAuthority, _ *WorkerEvidenceInput) { a.CredentialRevision = uuid.NewString() },
		func(_ *WorkerAuthority, in *WorkerEvidenceInput) { in.OriginalReceiptID = uuid.NewString() },
		func(_ *WorkerAuthority, in *WorkerEvidenceInput) { in.ExecutionID = uuid.NewString() },
		func(_ *WorkerAuthority, in *WorkerEvidenceInput) { in.ExecutionRevision = uuid.NewString() },
		func(_ *WorkerAuthority, in *WorkerEvidenceInput) { in.GrantID = uuid.NewString() },
		func(_ *WorkerAuthority, in *WorkerEvidenceInput) { in.ServerID = uuid.NewString() },
	} {
		x, y := a, in
		change(&x, &y)
		if _, err := s.CommitWorkerEvidence(t.Context(), x, y); err == nil {
			t.Fatal("foreign evidence accepted")
		}
	}
	s, a, _, result := workerResultFixture(t, "recovery", "completed")
	receipt, err := s.CommitWorkerResult(t.Context(), a, result)
	if err != nil {
		t.Fatal(err)
	}
	in.ServerID, in.ExecutionID, in.ExecutionRevision, in.GrantID, in.OriginalReceiptID = result.ServerID, result.ExecutionID, result.ExecutionRevision, result.GrantID, receipt.ReceiptID
	if _, err := s.CommitWorkerEvidence(t.Context(), a, in); !errors.Is(err, ErrWorkerExecutionConflict) {
		t.Fatal("late evidence attached to known completion", err)
	}
}

func TestWorkerEvidenceSnapshotIsFrozenAndRejectsCorruption(t *testing.T) {
	s, a, in := workerEvidenceFixture(t, "recovery")
	first, err := s.CommitWorkerEvidence(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	in.EvidenceID = uuid.NewString()
	if _, err := s.CommitWorkerEvidence(t.Context(), a, in); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), in.ExecutionID)
	r.Evidence[0].Input.Payload.Ciphertext[0] ^= 1
	sink := &collectionTestSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(workerDispatchSnapshotMagic)) {
		t.Fatal("snapshot does not preserve the delivery worker format")
	}
	if bytes.Contains(sink.Bytes(), []byte("private-worker-result-canary")) {
		t.Fatal("snapshot contains plaintext worker results")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	saved := i.WorkerExecutions.Records[in.ExecutionID]
	if len(saved.Evidence) != 1 || saved.Evidence[0].Receipt != first {
		t.Fatal("snapshot was not frozen")
	}
	if err := validateWorkerExecutionImage(i); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*image){
		"old-format":    func(i *image) { i.Version = WorkerResultFormatVersion },
		"quota-counter": func(i *image) { i.WorkerExecutions.EvidenceBytes++ },
		"receipt": func(i *image) {
			r := i.WorkerExecutions.Records[in.ExecutionID]
			r.Evidence[0].Receipt.ReceiptID = uuid.NewString()
			i.WorkerExecutions.Records[in.ExecutionID] = r
		},
		"original": func(i *image) {
			r := i.WorkerExecutions.Records[in.ExecutionID]
			r.Evidence[0].Input.OriginalReceiptID = uuid.NewString()
			i.WorkerExecutions.Records[in.ExecutionID] = r
		},
		"index": func(i *image) {
			r := i.WorkerExecutions.Records[in.ExecutionID]
			r.Evidence[0].CommittedIndex = i.Index + 1
			i.WorkerExecutions.Records[in.ExecutionID] = r
		},
		"time": func(i *image) {
			r := i.WorkerExecutions.Records[in.ExecutionID]
			r.Evidence[0].RecordedAt = r.Result.RecordedAt.Add(-time.Second)
			i.WorkerExecutions.Records[in.ExecutionID] = r
		},
		"duplicate": func(i *image) {
			r := i.WorkerExecutions.Records[in.ExecutionID]
			r.Evidence = append(r.Evidence, r.Evidence[0])
			i.WorkerExecutions.Records[in.ExecutionID] = r
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := i
			copy.WorkerExecutions = i.WorkerExecutions.clone()
			change(&copy)
			if validateWorkerExecutionImage(copy) == nil {
				t.Fatal("corrupt evidence restored")
			}
		})
	}
	c := Command{Kind: "worker_evidence", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerEvidence: &WorkerEvidenceCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: in}}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerLifecycleFormatVersion || version == WorkerFinalizationFormatVersion || version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion || version == WorkerNotificationConditionFormatVersion || version == WorkerRecoveryRulesFormatVersion || version == WorkerDispatchFormatVersion) {
			t.Fatal("evidence format", version, err)
		}
	}
	c.Outcome = "unrelated"
	if validateCommand(c) == nil {
		t.Fatal("mixed evidence command admitted")
	}
}

func TestWorkerEvidenceGlobalByteLimitAndPagedRecovery(t *testing.T) {
	s := openWorkerMemory(t)
	first, job := workerExecutionFixture(t, s, "check")
	worker := workerPrincipal("evidence-capacity-worker")
	worker.Grants = []WorkerGrant{{JobTypeID: job.Current.Record.Key.ID, JobTypeUID: job.Current.Record.UID, Version: job.Current.Version,
		Category: "check", ResourceKind: "Monitor", ResourceIDs: []string{"*"}}}
	s.administrative = true
	policy := commitWorker(t, s, workerCommand(t, s, "bootstrap", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	a, err := s.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 50; n++ {
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
	response, err := s.CommitWorkerPoll(t.Context(), a, WorkerPollRequest{ServerID: server.ServerID, WorkerID: worker.ID,
		ClientSessionID: uuid.NewString(), PollSequence: 1, Capacity: 50, Limit: 50, Capabilities: []WorkerSessionCapability{{JobTypeID: job.Current.Record.Key.ID, Version: job.Current.Version, Category: "check"}}})
	if err != nil || len(response.Offers) != 50 {
		t.Fatal("capacity offers", err)
	}
	var inputs []WorkerEvidenceInput
	for _, offer := range response.Offers {
		start, err := s.CommitWorkerStart(t.Context(), a, WorkerStartRequest{ServerID: server.ServerID, SessionID: response.SessionID,
			ExecutionID: offer.ExecutionID, ExecutionRevision: offer.ExecutionRevision, LeaseID: offer.LeaseID, Mode: "begin"})
		if err != nil || start.Disposition != "granted" {
			t.Fatal("capacity grant", err)
		}
		r, _, _ := s.WorkerExecution(t.Context(), offer.ExecutionID)
		result, err := s.CommitWorkerResult(t.Context(), a, WorkerResultInput{ServerID: server.ServerID, ExecutionID: offer.ExecutionID,
			ExecutionRevision: offer.ExecutionRevision, GrantID: start.GrantID, Status: "noData", Digest: identity("capacity-result"), Payload: r.Intent.Payload})
		if err != nil {
			t.Fatal(err)
		}
		// A maximum-length ciphertext exercises encoded namespace and page costs.
		payload := r.Intent.Payload.Clone()
		payload.Ciphertext = bytes.Repeat([]byte{31}, MaxWorkerExecutionPayload+16)
		inputs = append(inputs, WorkerEvidenceInput{ServerID: server.ServerID, ExecutionID: offer.ExecutionID, ExecutionRevision: offer.ExecutionRevision,
			GrantID: start.GrantID, OriginalReceiptID: result.ReceiptID, EvidenceID: uuid.NewString(), Digest: identity("capacity-evidence"), Payload: payload})
	}
	accepted, exhausted := 0, false
	var original WorkerReceipt
	for _, in := range inputs {
		for n := 0; n < MaxWorkerEvidencePerExecution; n++ {
			if n > 0 {
				in.EvidenceID = uuid.NewString()
			}
			receipt, err := s.CommitWorkerEvidence(t.Context(), a, in)
			if errors.Is(err, ErrWorkerExecutionQuota) {
				exhausted = true
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if accepted == 0 {
				original = receipt
			}
			accepted++
		}
		if exhausted {
			break
		}
	}
	if !exhausted || accepted < 300 {
		t.Fatal("global byte quota was not exercised", accepted)
	}
	if repeat, err := s.CommitWorkerEvidence(t.Context(), a, inputs[0]); err != nil || repeat != original {
		t.Fatal("global capacity blocked replay", err)
	}
	if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal("invalid full namespace", err)
	}
	seen, evidence, pages, after := 0, 0, 0, ""
	for {
		page, err := s.WorkerExecutions(t.Context(), after, 100)
		if err != nil || len(page.Items) == 0 {
			t.Fatal("evidence traversal", err)
		}
		raw, err := json.Marshal(page)
		if err != nil || len(raw) > MaxWorkerExecutionPageBytes {
			t.Fatal("evidence escaped page bound", len(raw), err)
		}
		for _, r := range page.Items {
			if r.Intent.ID <= after {
				t.Fatal("page did not advance")
			}
			seen++
			evidence += len(r.Evidence)
		}
		pages++
		if !page.More {
			break
		}
		after = page.Next
	}
	if seen != 50 || evidence != accepted || pages < 10 {
		t.Fatal("lost evidence or unbounded traversal", seen, evidence, pages)
	}
}

func TestWorkerLifecycleCredentialRotationAndRevocation(t *testing.T) {
	s, old, start, result := workerResultFixture(t, "recovery", "unknown")
	policy, err := s.WorkerPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	worker := policy.Workers[old.WorkerID]
	worker.Grants, worker.GrantRevision = nil, uuid.NewString()
	worker.TokenSHA256, worker.CredentialRevision = authenticationVerifier("rotated-lifecycle-worker"), uuid.NewString()
	s.administrative = true
	policy = commitWorker(t, s, workerCommand(t, s, "upsert", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	request := heartbeatFromResult(start, result)
	if _, err := s.CommitWorkerHeartbeat(t.Context(), old, request); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("obsolete credential continued work", err)
	}
	fresh, err := s.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.CommitWorkerHeartbeat(t.Context(), fresh, request)
	if err != nil || response.Accepted {
		t.Fatal("removed grant continued", err)
	}
	receipt, err := s.CommitWorkerResult(t.Context(), fresh, result)
	if err != nil {
		t.Fatal(err)
	}
	in := WorkerEvidenceInput{ServerID: result.ServerID, ExecutionID: result.ExecutionID, ExecutionRevision: result.ExecutionRevision,
		GrantID: result.GrantID, OriginalReceiptID: receipt.ReceiptID, EvidenceID: uuid.NewString(), Digest: identity("rotated-evidence"), Payload: result.Payload}
	if _, err := s.CommitWorkerEvidence(t.Context(), old, in); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("obsolete credential appended evidence", err)
	}
	if _, err := s.CommitWorkerEvidence(t.Context(), fresh, in); err != nil {
		t.Fatal("removed grant discarded original evidence", err)
	}
	worker.Revoked, worker.CredentialRevision = true, uuid.NewString()
	s.administrative = true
	policy = commitWorker(t, s, workerCommand(t, s, "upsert", worker))
	s.administrative = false
	if wait := time.Until(policy.UpdatedAt); wait > 0 {
		time.Sleep(wait + time.Millisecond)
	}
	if _, err := s.CommitWorkerEvidence(t.Context(), fresh, in); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("revoked credential replayed evidence", err)
	}
	if _, err := s.CommitWorkerHeartbeat(t.Context(), fresh, request); !errors.Is(err, ErrWorkerAuthorityDenied) {
		t.Fatal("revoked credential read heartbeat", err)
	}
}
