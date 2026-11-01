//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerRetirementFixture(t *testing.T) (*Store, WorkerAuthority, WorkerPollRequest, WorkerStartRequest, WorkerResultInput) {
	t.Helper()
	s, a, _, original := workerResultFixture(t, "check", "success")
	if _, err := s.CommitWorkerResult(t.Context(), a, original); err != nil {
		t.Fatal(err)
	}
	old, _, _ := s.WorkerExecution(t.Context(), original.ExecutionID)
	in := old.Intent.Clone()
	var err error
	in.ID, err = s.AllocateWorkerCheckID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = uuid.NewString()
	in.Generation++
	in.Scheduled, in.Deadline = time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-check-parameters"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	s.fsm.mu.RLock()
	session := s.fsm.image.WorkerSessions.Sessions[a.WorkerUID].clone()
	s.fsm.mu.RUnlock()
	request := session.Request
	request.SessionID, request.PollSequence = session.Response.SessionID, request.PollSequence+1
	poll, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil || len(poll.Offers) != 1 || poll.Offers[0].ExecutionID != in.ID {
		t.Fatal("numbered check offer", err)
	}
	startRequest := workerStartFromOffer(poll)
	start, err := s.CommitWorkerStart(t.Context(), a, startRequest)
	if err != nil || start.Disposition != "granted" {
		t.Fatal("numbered check start", err)
	}
	input := original
	input.ExecutionID, input.ExecutionRevision, input.GrantID = in.ID, in.Revision, start.GrantID
	input.Payload, err = catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution", ID: in.ID, UID: in.MonitorUID, Revision: in.Revision, Purpose: "worker-outcome-test"}, []byte("private-check-outcome"))
	if err != nil {
		t.Fatal(err)
	}
	return s, a, request, startRequest, input
}

func TestWorkerCheckRetentionCountAndConcurrentReplies(t *testing.T) {
	s, a, request, _, input := workerRetirementFixture(t)
	input.Status = "noData"
	pinnedAuthority, pinnedRequest, pinnedInput := a, request, input
	pinnedResponse := s.fsm.image.WorkerSessions.Sessions[a.WorkerUID].Response.clone()
	var first WorkerResultInput
	for n := 0; n < MaxWorkerCheckReceipts+3; n++ {
		if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
			t.Fatal(n, err)
		}
		if n == 0 {
			// Keep the first worker's response replayable while another worker
			// completes enough checks to exceed the receipt-count target.
			policy, err := s.WorkerPolicy(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p := workerPrincipal("retention-worker")
			p.Grants = policy.Workers[a.WorkerID].Clone().Grants
			s.administrative = true
			updated := commitWorker(t, s, workerCommand(t, s, "upsert", p))
			s.administrative = false
			if wait := time.Until(updated.UpdatedAt); wait > 0 {
				time.Sleep(wait + time.Millisecond)
			}
			a, err = s.AuthenticateWorker(t.Context(), p.TokenSHA256, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			pinnedAuthority, err = s.AuthenticateWorker(t.Context(), policy.Workers[pinnedAuthority.WorkerID].TokenSHA256, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			request.WorkerID, request.ClientSessionID = p.ID, uuid.NewString()
			request.SessionID, request.PollSequence = "", 0
		}
		if n == 1 {
			first = input
		}
		if n == MaxWorkerCheckReceipts+2 {
			break
		}
		old, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
		in := old.Intent.Clone()
		var err error
		in.ID, err = s.AllocateWorkerCheckID(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		in.Revision = uuid.NewString()
		in.Generation++
		in.Scheduled = time.Now().UTC()
		in.Deadline = in.Scheduled.Add(time.Hour)
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("bounded-check"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		request.PollSequence++
		poll, err := s.CommitWorkerPoll(t.Context(), a, request)
		if err != nil || len(poll.Offers) != 1 {
			t.Fatal(n, err)
		}
		request.SessionID = poll.SessionID
		start, err := s.CommitWorkerStart(t.Context(), a, workerStartFromOffer(poll))
		if err != nil {
			t.Fatal(err)
		}
		input.ExecutionID, input.ExecutionRevision, input.GrantID = in.ID, in.Revision, start.GrantID
		input.Payload, err = catalogSealer(t).Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution", ID: in.ID, UID: in.MonitorUID, Revision: in.Revision, Purpose: "worker-outcome-test"}, []byte("no-data"))
		if err != nil {
			t.Fatal(err)
		}
	}
	before := cloneImageExtensions(s.fsm.image).WorkerSLO
	errorsFromReplies := make(chan error, 16)
	for range 16 {
		go func() { _, err := s.CommitWorkerResult(t.Context(), a, first); errorsFromReplies <- err }()
	}
	if n, err := s.RetireWorkerChecks(t.Context(), time.Now().UTC(), MaxWorkerCheckRetirements); err != nil || n != 2 {
		t.Fatal("count retention", n, err)
	}
	for range 16 {
		if err := <-errorsFromReplies; err != nil && !errors.Is(err, ErrWorkerCheckExpired) {
			t.Fatal("concurrent reply", err)
		}
	}
	if !reflect.DeepEqual(before, s.fsm.image.WorkerSLO) {
		t.Fatal("retirement or duplicate result changed aggregate")
	}
	if len(s.fsm.image.WorkerExecutions.Records) != MaxWorkerCheckReceipts+2 {
		t.Fatal("unsequenced receipt or active target lost")
	}
	if response, err := s.CommitWorkerPoll(t.Context(), pinnedAuthority, pinnedRequest); err != nil || !reflect.DeepEqual(response, pinnedResponse) {
		t.Fatal("live poll response lost its completed check", err)
	}
	if _, found, _ := s.WorkerExecution(t.Context(), pinnedInput.ExecutionID); !found {
		t.Fatal("live poll response did not pin its completed check")
	}
	if n, err := s.RetireWorkerChecks(t.Context(), pinnedResponse.SessionExpiresAt, MaxWorkerCheckRetirements); err != nil || n != 1 {
		t.Fatal("expired poll kept count-based pin", n, err)
	}
	if !s.fsm.workerCheckExpired(pinnedInput.ExecutionID) {
		t.Fatal("expired pin did not release its completed check")
	}
	if _, found, _ := s.WorkerExecution(t.Context(), input.ExecutionID); !found {
		t.Fatal("newest receipt retired")
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCheckRetentionKeepsActions(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		for _, status := range []string{"accepted", "unknown"} {
			t.Run(category+"/"+status, func(t *testing.T) {
				s, a, _, input := workerResultFixture(t, category, status)
				if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
					t.Fatal(err)
				}
				saved, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				if status == "unknown" {
					evidence := WorkerEvidenceInput{ServerID: input.ServerID, ExecutionID: input.ExecutionID,
						ExecutionRevision: input.ExecutionRevision, GrantID: input.GrantID,
						OriginalReceiptID: saved.Result.Receipt.ReceiptID, EvidenceID: uuid.NewString(),
						Digest: identity("action-evidence"), Payload: input.Payload.Clone()}
					if _, err := s.CommitWorkerEvidence(t.Context(), a, evidence); err != nil {
						t.Fatal(err)
					}
					saved, _, _ = s.WorkerExecution(t.Context(), input.ExecutionID)
				}
				id, err := s.AllocateWorkerCheckID(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if n, err := s.RetireWorkerChecks(t.Context(), time.Now().UTC().Add(24*time.Hour), MaxWorkerCheckRetirements); err != nil || n != 1 {
					t.Fatal(n, err)
				}
				if !s.fsm.workerCheckExpired(id) {
					t.Fatal("unused reservation retained")
				}
				got, found, err := s.WorkerExecution(t.Context(), input.ExecutionID)
				if err != nil || !found || !reflect.DeepEqual(got, saved) {
					t.Fatal("action outcome expired", err)
				}
			})
		}
	}
}

func TestWorkerCheckRetentionReclaimsLateEvidence(t *testing.T) {
	for _, evidenceFirst := range []bool{true, false} {
		name := "retirement-before-evidence"
		if evidenceFirst {
			name = "evidence-before-retirement"
		}
		t.Run(name, func(t *testing.T) {
			s, a, _, _, input := workerRetirementFixture(t)
			input.Status = "noData"
			receipt, err := s.CommitWorkerResult(t.Context(), a, input)
			if err != nil {
				t.Fatal(err)
			}
			r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			before, _ := s.Get(r.Intent.MonitorID)
			slo := cloneImageExtensions(s.fsm.image).WorkerSLO
			evidence := WorkerEvidenceInput{ServerID: input.ServerID, ExecutionID: input.ExecutionID,
				ExecutionRevision: input.ExecutionRevision, GrantID: input.GrantID,
				OriginalReceiptID: receipt.ReceiptID, EvidenceID: uuid.NewString(),
				Digest: identity("check-evidence"), Payload: input.Payload.Clone()}
			if evidenceFirst {
				for range MaxWorkerEvidencePerExecution {
					evidence.EvidenceID = uuid.NewString()
					if _, err := s.CommitWorkerEvidence(t.Context(), a, evidence); err != nil {
						t.Fatal(err)
					}
				}
				if s.fsm.image.WorkerExecutions.EvidenceBytes <= 0 {
					t.Fatal("evidence bytes were not charged")
				}
			}
			if n, err := s.RetireWorkerChecks(t.Context(), r.Result.RecordedAt.Add(WorkerCheckReceiptLifetime), 1); err != nil || n != 1 {
				t.Fatal("evidence blocked retirement", n, err)
			}
			if s.fsm.image.WorkerExecutions.EvidenceBytes != 0 {
				t.Fatal("retired evidence capacity was not reclaimed")
			}
			if _, err := s.CommitWorkerEvidence(t.Context(), a, evidence); !errors.Is(err, ErrWorkerExecutionConflict) {
				t.Fatal("retired evidence was recreated or falsely acknowledged", err)
			}
			after, _ := s.Get(r.Intent.MonitorID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(slo, s.fsm.image.WorkerSLO) {
				t.Fatal("evidence retirement changed health or SLO state")
			}
			restored, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			if err := validateImageExtensions(restored); err != nil {
				t.Fatal("retired evidence accounting does not restore", err)
			}
		})
	}
}

func TestWorkerCheckReservationExpiryAndAdmissionFence(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "check")
	id, err := s.AllocateWorkerCheckID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := s.fsm.image.WorkerChecks.Reservations[id]
	at := r.CreatedAt.Add(WorkerCheckReservationLifetime)
	if s.fsm.workerCheckExpired(id) {
		t.Fatal("reservation mistaken for retired check")
	}
	if n, err := s.RetireWorkerChecks(t.Context(), at.Add(-time.Nanosecond), 1); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if n, err := s.RetireWorkerChecks(t.Context(), at, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if !s.fsm.workerCheckExpired(id) {
		t.Fatal("retirement did not preserve issuance")
	}
	in.ID = id
	in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), []byte("private-check-parameters"))
	if err != nil {
		t.Fatal(err)
	}
	c := WorkerExecutionCommand{CheckReserved: true, SchedulingVersion: 1, Intent: in, OwnerEpoch: s.executorSession}
	// An admission timestamped before expiry but committed afterwards cannot
	// consume the retired reservation or recreate its identity.
	result := submit(t, s, Command{Kind: "worker_execution", At: r.CreatedAt.Add(time.Millisecond), commandExtensions: commandExtensions{WorkerExecution: &c}})[0]
	if !errors.Is(result.Err, ErrWorkerCheckExpired) {
		t.Fatal("retired reservation admitted", result.Err)
	}
	next, err := s.AllocateWorkerCheckID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	epoch, sequence, _ := parseWorkerCheckID(id)
	if next != workerCheckID(epoch, sequence+1) {
		t.Fatal("allocation reused an identity")
	}
	if s.fsm.workerCheckExpired(workerCheckID(epoch, sequence+2)) || s.fsm.workerCheckExpired(workerCheckID(uuid.NewString(), 1)) {
		t.Fatal("unissued identity reported expired")
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCheckRetentionPreservesStateAndExpiredPollFence(t *testing.T) {
	s, a, request, start, input := workerRetirementFixture(t)
	if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	monitor, _ := s.Get(r.Intent.MonitorID)
	slo := cloneImageExtensions(s.fsm.image).WorkerSLO
	used := s.fsm.image.WorkerExecutions.EncodedBytes
	at := r.Result.RecordedAt.Add(WorkerCheckReceiptLifetime)
	if n, err := s.RetireWorkerChecks(t.Context(), at.Add(-time.Nanosecond), 1); err != nil || n != 0 {
		t.Fatal("early retirement", n, err)
	}
	if n, err := s.RetireWorkerChecks(t.Context(), at, 1); err != nil || n != 1 {
		t.Fatal("retirement", n, err)
	}
	if _, found, err := s.WorkerExecution(t.Context(), input.ExecutionID); err != nil || found {
		t.Fatal("record still retained", err)
	}
	after, _ := s.Get(r.Intent.MonitorID)
	if !reflect.DeepEqual(monitor, after) || !reflect.DeepEqual(slo, s.fsm.image.WorkerSLO) {
		t.Fatal("cleanup changed monitor or SLO accounting")
	}
	if s.fsm.image.WorkerExecutions.EncodedBytes >= used {
		t.Fatal("execution capacity not reclaimed")
	}
	requireWorkerProjectionView(t, s, monitor.ID)
	session := s.fsm.image.WorkerSessions.Sessions[a.WorkerUID]
	if session.ReplyExpiredAt == nil || len(session.Response.Offers) != 0 {
		t.Fatal("expired response still pins removed record")
	}
	// Moving the request clock backwards must not replay a successful response
	// with its assignments silently removed, including the opening request.
	for _, replayAt := range []time.Time{at, session.UpdatedAt} {
		if result := workerPollAt(t, s, a, request, replayAt); !errors.Is(result.Err, ErrWorkerSessionExpired) {
			t.Fatal("expired response replay", result.Err)
		}
	}
	if _, err := s.CommitWorkerStart(t.Context(), a, start); !errors.Is(err, ErrWorkerCheckExpired) {
		t.Fatal("expired Start", err)
	}
	if _, err := s.CommitWorkerResult(t.Context(), a, input); !errors.Is(err, ErrWorkerCheckExpired) {
		t.Fatal("expired Result", err)
	}
	if _, err := s.CommitWorkerExecution(t.Context(), r.Intent); !errors.Is(err, ErrWorkerCheckExpired) {
		t.Fatal("retired check readmitted", err)
	}
	if n, err := s.RetireWorkerChecks(t.Context(), at, 1); err != nil || n != 0 {
		t.Fatal("duplicate cleanup", n, err)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal("snapshot validation", err)
	}
	encoded := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(encoded, []byte(workerDispatchSnapshotMagic)) {
		t.Fatal("snapshot does not preserve the delivery worker format")
	}
	if bytes.Contains(encoded, []byte("private-check-parameters")) {
		t.Fatal("snapshot contains plaintext check parameters")
	}
	restored, ledger, err := decodeSnapshot(bytes.NewReader(encoded), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if err := validateImageExtensions(restored); err != nil {
		t.Fatal(err)
	}
	f := &machine{image: restored}
	if !f.workerCheckExpired(input.ExecutionID) {
		t.Fatal("snapshot lost retired identity")
	}
	if !reflect.DeepEqual(restored.WorkerSLO, slo) {
		t.Fatal("snapshot changed aggregate")
	}
	if result := f.applyWorkerStart(WorkerStartCommand{OwnerEpoch: restored.LocalExecutorSession, Authority: a, Request: start}, time.Now().UTC(), restored.Version); !errors.Is(result.Err, ErrWorkerCheckExpired) {
		t.Fatal("snapshot regranted expired check", result.Err)
	}
}

func TestWorkerCheckRetentionMissingMeasurementAndAuthentication(t *testing.T) {
	s, a, _, start, input := workerRetirementFixture(t)
	commitWorkerResultWithoutObservation(t, s, a, input)
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	report := workerSLOReport(t, s, time.Now())
	if n, err := s.RetireWorkerChecks(t.Context(), r.Result.RecordedAt.Add(WorkerCheckReceiptLifetime), 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if !reflect.DeepEqual(report, workerSLOReport(t, s, time.Now())) {
		t.Fatal("retirement improved missing-measurement attainment")
	}
	for _, field := range []string{"credential", "worker", "epoch", "server"} {
		bad, outcome, request := a, input, start
		switch field {
		case "credential":
			bad.CredentialRevision = uuid.NewString()
		case "worker":
			bad.WorkerUID = uuid.NewString()
		case "epoch":
			bad.Epoch = uuid.NewString()
		case "server":
			outcome.ServerID, request.ServerID = "wrong-server", "wrong-server"
		}
		if _, err := s.WorkerResultExecution(t.Context(), bad, outcome.ServerID, outcome.ExecutionID, outcome.GrantID); err == nil || errors.Is(err, ErrWorkerCheckExpired) {
			t.Fatal("unauthorized expiry", field, err)
		}
		if _, err := s.CommitWorkerStart(t.Context(), bad, request); err == nil || errors.Is(err, ErrWorkerCheckExpired) {
			t.Fatal("unauthorized start expiry", field, err)
		}
	}
}

func TestWorkerCheckRetirementCommandsAndCorruption(t *testing.T) {
	s := openCatalogMemory(t)
	id, err := s.AllocateWorkerCheckID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	at := s.fsm.image.WorkerChecks.Reservations[id].CreatedAt.Add(WorkerCheckReservationLifetime)
	c := WorkerCheckRetireCommand{OwnerEpoch: s.executorSession, IDs: []string{id}}
	command := Command{Kind: "worker_check_retire", At: at, commandExtensions: commandExtensions{WorkerCheckRetire: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion || version == WorkerNotificationConditionFormatVersion || version == WorkerRecoveryRulesFormatVersion || version == WorkerDispatchFormatVersion) {
			t.Fatal("format gate", version, err)
		}
	}
	for _, bad := range []Command{
		{Kind: "worker_check_retire", At: at},
		{Kind: "barrier", At: at, commandExtensions: commandExtensions{WorkerCheckRetire: &c}},
		{Kind: "worker_check_retire", At: at, MonitorID: "unowned", commandExtensions: commandExtensions{WorkerCheckRetire: &c}},
	} {
		if _, err := validateCommandExtensions(bad); err == nil {
			t.Fatal("unowned command accepted")
		}
	}
	for _, edit := range []func(*workerCheckImage){
		func(i *workerCheckImage) { i.HighWater = 0 },
		func(i *workerCheckImage) { i.Epoch = uuid.NewString() },
		func(i *workerCheckImage) { i.EncodedBytes-- },
		func(i *workerCheckImage) {
			r := i.Reservations[id]
			r.CommittedIndex = math.MaxUint64
			i.Reservations[id] = r
		},
	} {
		image := s.fsm.image
		image.imageExtensions = cloneImageExtensions(image)
		edit(image.WorkerChecks)
		if err := validateImageExtensions(image); err == nil {
			t.Fatal("corrupt reservation accepted")
		}
	}
	epoch, sequence, _ := parseWorkerCheckID(id)
	badBatch := c
	badBatch.IDs = []string{id, workerCheckID(epoch, sequence+1)}
	before := s.fsm.image.WorkerChecks.clone()
	if result := submit(t, s, Command{Kind: command.Kind, At: at, commandExtensions: commandExtensions{WorkerCheckRetire: &badBatch}})[0]; !errors.Is(result.Err, ErrWorkerExecutionConflict) {
		t.Fatal(result.Err)
	}
	if !reflect.DeepEqual(before, s.fsm.image.WorkerChecks) {
		t.Fatal("invalid final item partially retired batch")
	}
	if result := submit(t, s, command)[0]; result.Err != nil || result.WorkerChecksRetired != 1 {
		t.Fatal(result.Err)
	}
	if result := submit(t, s, command)[0]; result.Err != nil || result.WorkerChecksRetired != 0 {
		t.Fatal("idempotent retirement", result.Err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.AllocateWorkerCheckID(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWorkerCheckReservationBoundsAndOwner(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	commands := make([]Command, MaxWorkerCheckReservations)
	for n := range commands {
		c := WorkerCheckAllocateCommand{ProposedEpoch: uuid.NewString(), OwnerEpoch: s.executorSession}
		commands[n] = Command{Kind: "worker_check_allocate", At: at, commandExtensions: commandExtensions{WorkerCheckAllocate: &c}}
	}
	for len(commands) > 0 {
		n := min(len(commands), s.config.Storage.BatchSize)
		for _, result := range submit(t, s, commands[:n]...) {
			if result.Err != nil || result.WorkerCheckID == "" {
				t.Fatal(result.Err)
			}
		}
		commands = commands[n:]
	}
	if _, err := s.AllocateWorkerCheckID(t.Context()); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("reservation quota", err)
	}
	if n, err := s.RetireWorkerChecks(t.Context(), at.Add(WorkerCheckReservationLifetime), MaxWorkerCheckRetirements); err != nil || n != MaxWorkerCheckRetirements {
		t.Fatal("bounded maintenance", n, err)
	}
	if len(s.fsm.image.WorkerChecks.Reservations) != MaxWorkerCheckReservations-MaxWorkerCheckRetirements {
		t.Fatal("maintenance exceeded bound")
	}
	if _, err := s.AllocateWorkerCheckID(t.Context()); err != nil {
		t.Fatal("quota not reclaimed", err)
	}
	var ids []string
	for id := range s.fsm.image.WorkerChecks.Reservations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	c := WorkerCheckRetireCommand{OwnerEpoch: "stale-owner", IDs: ids[:1]}
	if result := submit(t, s, Command{Kind: "worker_check_retire", At: at.Add(time.Hour), commandExtensions: commandExtensions{WorkerCheckRetire: &c}})[0]; !errors.Is(result.Err, ErrWorkerSessionExpired) {
		t.Fatal("owner fence", result.Err)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}
