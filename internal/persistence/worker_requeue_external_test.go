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
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func workerPollAt(t *testing.T, s *Store, a WorkerAuthority, request WorkerPollRequest, at time.Time) Result {
	t.Helper()
	return sessionApply(t, s, a, request, at)
}

func TestWorkerRequeueExpiredOffersAllCategories(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, request, in, _ := workerOfferFixture(t, category)
			poll, err := s.CommitWorkerPoll(t.Context(), a, request)
			if err != nil || len(poll.Offers) != 1 {
				t.Fatal(err)
			}
			old, _, _ := s.WorkerExecution(t.Context(), in.ID)
			start := workerStartFromOffer(poll)
			at := old.Lifecycle.OfferDeadline
			monitor, _ := s.Get(in.MonitorID)
			slo := cloneImageExtensions(s.fsm.image).WorkerSLO
			used := s.fsm.image.WorkerExecutions.EncodedBytes
			if n, err := s.RequeueWorkerExecutions(t.Context(), at.Add(-time.Nanosecond), 1); err != nil || n != 0 {
				t.Fatal("unexpired offer requeued", n, err)
			}
			if n, err := s.RequeueWorkerExecutions(t.Context(), at, 1); err != nil || n != 1 {
				t.Fatal("expired offer not requeued", n, err)
			}
			queued, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if queued.Lifecycle != nil || len(queued.RetiredOffers) != 1 || !reflect.DeepEqual(queued.Intent, old.Intent) || queued.SLOEpoch != old.SLOEpoch {
				t.Fatal("requeue changed execution identity or lost the previous lease")
			}
			after, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(after, monitor) || !reflect.DeepEqual(slo, s.fsm.image.WorkerSLO) || used != s.fsm.image.WorkerExecutions.EncodedBytes {
				t.Fatal("requeue changed health, action, SLO accounting or reserved bytes")
			}
			if err := s.VerifyWorkerPollResponse(t.Context(), a, poll); err == nil {
				t.Fatal("retired plaintext offer was still readable")
			}
			request.SessionID, request.PollSequence = poll.SessionID, 2
			next := workerPollAt(t, s, a, request, at.Add(time.Millisecond))
			if next.Err != nil || next.WorkerPoll == nil || len(next.WorkerPoll.Offers) != 1 || next.WorkerPoll.Offers[0].LeaseID == start.LeaseID {
				t.Fatal("replacement lease", next.Err)
			}
			newStart := workerStartFromOffer(*next.WorkerPoll)
			granted := workerStartAt(t, s, a, newStart, at.Add(2*time.Millisecond))
			if granted.Err != nil || granted.WorkerStart.Disposition != "granted" {
				t.Fatal("replacement could not start", granted.Err)
			}
			for _, mode := range []string{"begin", "reconcile"} {
				start.Mode = mode
				rejected := workerStartAt(t, s, a, start, at.Add(3*time.Millisecond))
				if rejected.Err != nil || rejected.WorkerStart == nil || rejected.WorkerStart.Disposition != "rejected" || rejected.WorkerStart.GrantID != "" || rejected.WorkerStart.ReceiptID != "" {
					t.Fatal("old lease gained successor authority", rejected.Err)
				}
			}
			// A duplicate maintenance command after Start must not clear its grant.
			cmd := workerRequeueCommand(old, s.executorSession)
			if result := submit(t, s, Command{Kind: "worker_requeue", At: at.Add(4 * time.Millisecond), commandExtensions: commandExtensions{WorkerRequeue: &cmd}})[0]; result.Err != nil || !result.Allowed {
				t.Fatal("original requeue retry", result.Err)
			}
			current, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if current.Lifecycle == nil || current.Lifecycle.GrantID != granted.WorkerStart.GrantID || current.Lifecycle.Phase != "started" {
				t.Fatal("retry removed an issued grant")
			}
			if n, err := s.RequeueWorkerExecutions(t.Context(), at.Add(time.Minute), 1); err != nil || n != 0 {
				t.Fatal("started execution requeued", n, err)
			}
			if err := validateImageExtensions(s.fsm.image); err != nil {
				t.Fatal("snapshot", err)
			}
		})
	}
}

func TestWorkerRequeuePreviousOwnerAndControlFence(t *testing.T) {
	for _, phase := range []string{"queued", "offered", "started", "disabled"} {
		t.Run(phase, func(t *testing.T) {
			s, a, request, in, _ := workerOfferFixture(t, "recovery")
			if phase != "queued" {
				poll, err := s.CommitWorkerPoll(t.Context(), a, request)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "started" {
					if _, err := s.CommitWorkerStart(t.Context(), a, workerStartFromOffer(poll)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if phase == "disabled" {
				m, _ := s.Get(in.MonitorID)
				m.Policy.Enabled = false
				mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: &in.Guard, At: time.Now().UTC()})
			}
			old, _, _ := s.WorkerExecution(t.Context(), in.ID)
			owner, at := uuid.NewString(), time.Now().UTC().Add(time.Millisecond)
			if result := submit(t, s, Command{Kind: "local_session", ExecutorSession: owner, At: at})[0]; result.Err != nil || !result.Allowed {
				t.Fatal("owner transition", result.Err)
			}
			c := workerRequeueCommand(old, owner)
			result := submit(t, s, Command{Kind: "worker_requeue", At: at.Add(time.Millisecond), commandExtensions: commandExtensions{WorkerRequeue: &c}})[0]
			want := phase == "queued" || phase == "offered"
			if (result.Err == nil && result.Allowed) != want {
				t.Fatalf("%s: %v", phase, result.Err)
			}
			current, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if want && (current.OwnerEpoch != owner || current.Lifecycle != nil || !reflect.DeepEqual(current.Intent, old.Intent)) {
				t.Fatal("owner transfer changed the queued intent")
			}
			if !want && !reflect.DeepEqual(current, old) {
				t.Fatal("ineligible execution changed")
			}
			if err := validateImageExtensions(s.fsm.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerRequeueOfferLimitFinalizesWithoutStart(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, request, in, _ := workerOfferFixture(t, category)
			at := time.Now().UTC()
			for n := 0; n <= MaxWorkerRetiredOffers; n++ {
				poll := workerPollAt(t, s, a, request, at)
				if poll.Err != nil || poll.WorkerPoll == nil || len(poll.WorkerPoll.Offers) != 1 {
					t.Fatal("poll", n, poll.Err)
				}
				request.SessionID, request.PollSequence = poll.WorkerPoll.SessionID, request.PollSequence+1
				r, _, _ := s.WorkerExecution(t.Context(), in.ID)
				at = r.Lifecycle.OfferDeadline
				count, err := s.RequeueWorkerExecutions(t.Context(), at, 1)
				want := 1
				if n == MaxWorkerRetiredOffers {
					want = 0
				}
				if err != nil || count != want {
					t.Fatal("requeue bound", n, count, err)
				}
				at = at.Add(time.Millisecond)
			}
			if n, err := s.FinalizeWorkerExecutions(t.Context(), at, 1); err != nil || n != 1 {
				t.Fatal("offer exhaustion", n, err)
			}
			r, _, _ := s.WorkerExecution(t.Context(), in.ID)
			if r.Finalization == nil || r.Finalization.Reason != "offer_limit" || r.Finalization.TimedOut || r.Finalization.Receipt.Disposition != "finalized" || r.Lifecycle.GrantID != "" {
				t.Fatal("exhaustion invented execution or timeout")
			}
			if err := validateImageExtensions(s.fsm.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerRequeueSnapshotAndCommandBounds(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "notification")
	poll, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), in.ID)
	c := workerRequeueCommand(r, s.executorSession)
	command := Command{Kind: "worker_requeue", At: r.Lifecycle.OfferDeadline, commandExtensions: commandExtensions{WorkerRequeue: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion || version == ActionWorkerFormatVersion) {
			t.Fatal("format gate", version, err)
		}
	}
	for _, bad := range []Command{{Kind: "barrier", commandExtensions: commandExtensions{WorkerRequeue: &c}}, {Kind: "worker_requeue"}, {Kind: "worker_requeue", MonitorID: "unexpected", commandExtensions: commandExtensions{WorkerRequeue: &c}}} {
		bad.At = command.At
		if _, err := validateCommandExtensions(bad); err == nil {
			t.Fatal("unowned command fields accepted")
		}
	}
	if result := submit(t, s, command)[0]; result.Err != nil || !result.Allowed {
		t.Fatal("requeue", result.Err)
	}
	snapshot := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(snapshot, []byte(actionWorkerSnapshotMagic)) {
		t.Fatal("snapshot format")
	}
	restored, _, err := decodeSnapshot(bytes.NewReader(snapshot), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateImageExtensions(restored); err != nil {
		t.Fatal("retired offer broke retained session replay", err)
	}
	if len(restored.WorkerSessions.Sessions[a.WorkerUID].Response.Offers) != len(poll.Offers) {
		t.Fatal("requeue rewrote original poll response")
	}
	for _, edit := range []func(*WorkerExecutionRecord){
		func(r *WorkerExecutionRecord) { r.RetiredOffers[0].GrantID = "executable" },
		func(r *WorkerExecutionRecord) { r.RetiredOffers[0].Phase = "offered" },
		func(r *WorkerExecutionRecord) { r.RetiredOffers[0].StartedAt = command.At },
		func(r *WorkerExecutionRecord) { r.RetiredOffers[0].Heartbeat = &WorkerHeartbeatState{At: command.At} },
		func(r *WorkerExecutionRecord) { r.RetiredOffers = append(r.RetiredOffers, r.RetiredOffers[0]) },
	} {
		image := restored
		image.imageExtensions = cloneImageExtensions(restored)
		record := image.WorkerExecutions.Records[in.ID]
		edit(&record)
		image.WorkerExecutions.Records[in.ID] = record
		if err := validateImageExtensions(image); err == nil {
			t.Fatal("invalid retired lease accepted")
		}
	}
	if len(s.fsm.image.WorkerExecutions.Records[in.ID].RetiredOffers) != 1 || s.fsm.image.WorkerExecutions.Records[in.ID].RetiredOffers[0].GrantID != "" {
		t.Fatal("snapshot clone borrowed live lease state")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.RequeueWorkerExecutions(ctx, command.At, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, limit := range []int{0, MaxWorkerRequeuesPerTick + 1} {
		if _, err := s.RequeueWorkerExecutions(t.Context(), command.At, limit); !errors.Is(err, ErrWorkerExecutionInvalid) {
			t.Fatal(err)
		}
	}
}

func TestWorkerRequeueConcurrentDuplicateCommands(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "recovery")
	if _, err := s.CommitWorkerPoll(t.Context(), a, request); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), in.ID)
	c := workerRequeueCommand(r, s.executorSession)
	command := Command{Kind: "worker_requeue", At: r.Lifecycle.OfferDeadline, commandExtensions: commandExtensions{WorkerRequeue: &c}}
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Submit(t.Context(), []Command{command})
			if err != nil || len(result) != 1 || result[0].Err != nil || !result[0].Allowed {
				t.Errorf("duplicate requeue: %v %v", err, result)
			}
		}()
	}
	wg.Wait()
	current, _, _ := s.WorkerExecution(t.Context(), in.ID)
	if len(current.RetiredOffers) != 1 || current.Lifecycle != nil {
		t.Fatal("duplicate maintenance consumed another attempt")
	}
}

func TestWorkerCheckNextGenerationAdmission(t *testing.T) {
	s, authority, _, input := workerResultFixture(t, "check", "success")
	original, _, err := s.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.CommitWorkerResult(t.Context(), authority, input)
	if err != nil {
		t.Fatal(err)
	}
	next := original.Intent.Clone()
	next.ID, next.Revision = uuid.NewString(), uuid.NewString()
	next.Generation++
	next.Scheduled = time.Now().UTC()
	next.Deadline = next.Scheduled.Add(time.Minute)
	next.Payload, err = catalogSealer(t).Seal(t.Context(), next.Binding(s.nodeID), []byte("next-check-parameters"))
	if err != nil {
		t.Fatal(err)
	}
	// Replaying an older command must keep its original admission decision.
	oldCommand := WorkerExecutionCommand{Intent: next, OwnerEpoch: s.executorSession}
	if r := submit(t, s, Command{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &oldCommand}})[0]; !errors.Is(r.Err, ErrWorkerExecutionConflict) {
		t.Fatal("historical admission semantics changed", r.Err)
	}
	if _, err = s.CommitWorkerExecution(t.Context(), next); err != nil {
		t.Fatal("completed generation still occupies the monitor admission slot", err)
	}
	if repeated, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil || repeated != receipt {
		t.Fatal("new check replaced the original result receipt", err)
	}
	if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal("successive generations cannot survive a snapshot", err)
	}
	older := s.fsm.image
	older.Version = WorkerSLOFormatVersion
	if err := validateWorkerExecutionImage(older); err == nil {
		t.Fatal("older snapshot format accepted successive generations")
	}
}

func TestWorkerRequeueRejectsBackwardsOwnerTime(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "check")
	if _, err := s.CommitWorkerPoll(t.Context(), a, request); err != nil {
		t.Fatal(err)
	}
	offered, _, _ := s.WorkerExecution(t.Context(), in.ID)
	at := offered.Lifecycle.OfferDeadline
	if n, err := s.RequeueWorkerExecutions(t.Context(), at, 1); err != nil || n != 1 {
		t.Fatal("retire offer", n, err)
	}
	queued, _, _ := s.WorkerExecution(t.Context(), in.ID)
	owner := uuid.NewString()
	if r := submit(t, s, Command{Kind: "local_session", ExecutorSession: owner, At: at.Add(time.Millisecond)})[0]; r.Err != nil {
		t.Fatal(r.Err)
	}
	c := workerRequeueCommand(queued, owner)
	r := submit(t, s, Command{Kind: "worker_requeue", At: at.Add(-time.Nanosecond), commandExtensions: commandExtensions{WorkerRequeue: &c}})[0]
	if !errors.Is(r.Err, ErrWorkerExecutionConflict) {
		t.Fatal("clock rollback transferred an execution before its recorded retirement", r.Err)
	}
	current, _, _ := s.WorkerExecution(t.Context(), in.ID)
	if !reflect.DeepEqual(queued, current) {
		t.Fatal("rejected owner transfer changed committed work")
	}
}

func TestWorkerRequeueRetiredLeaseCannotBeReused(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "notification")
	poll, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.WorkerExecution(t.Context(), in.ID)
	at := r.Lifecycle.OfferDeadline
	if n, err := s.RequeueWorkerExecutions(t.Context(), at, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	request.SessionID, request.PollSequence = poll.SessionID, 2
	request.Capacity, request.Limit = 1, 1
	c := WorkerSessionCommand{SelectionVersion: 2, NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request,
		ProposedSessionID: uuid.NewString(), ProposedLeaseIDs: []string{r.Lifecycle.LeaseID}}
	pollAt := at.Add(time.Millisecond)
	c.Maintenance, err = s.prepareWorkerPollMaintenance(t.Context(), c, pollAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneImageExtensions(s.fsm.image)
	result := submit(t, s, Command{Kind: "worker_poll", At: pollAt, commandExtensions: commandExtensions{WorkerSession: &c}})[0]
	if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
		t.Fatal("retired lease was reused", result.Err)
	}
	if !reflect.DeepEqual(before, cloneImageExtensions(s.fsm.image)) {
		t.Fatal("failed lease allocation changed session or execution")
	}
}

func TestWorkerRequeuePreservesTerminalAndUnknownWork(t *testing.T) {
	for category, statuses := range map[string][]string{
		"check": {"success", "noData"}, "recovery": {"completed", "unknown"}, "notification": {"delivered", "unknown"},
	} {
		for _, status := range statuses {
			t.Run(category+"/"+status, func(t *testing.T) {
				s, a, _, input := workerResultFixture(t, category, status)
				if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
					t.Fatal(err)
				}
				r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				at, owner := time.Now().UTC().Add(time.Second), uuid.NewString()
				if result := submit(t, s, Command{Kind: "local_session", ExecutorSession: owner, At: at})[0]; result.Err != nil {
					t.Fatal(result.Err)
				}
				c := workerRequeueCommand(r, owner)
				result := submit(t, s, Command{Kind: "worker_requeue", At: at, commandExtensions: commandExtensions{WorkerRequeue: &c}})[0]
				if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
					t.Fatal("terminal execution requeued", result.Err)
				}
				after, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
				if !reflect.DeepEqual(r, after) {
					t.Fatal("requeue changed original outcome or receipt")
				}
			})
		}
	}
}

func TestWorkerSchedulingCommandVersion(t *testing.T) {
	s, _, _, input, _ := workerOfferFixture(t, "check")
	for _, version := range []int{-1, 0, 1, 2} {
		c := WorkerExecutionCommand{SchedulingVersion: version, Intent: input, OwnerEpoch: s.executorSession}
		if err := c.validate(time.Now().UTC()); (err == nil) != (version == 0 || version == 1) {
			t.Fatal("scheduling version", version, err)
		}
	}
}

func TestWorkerSchedulingCompletedChecksReleaseTypeCapacity(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "check")
	sealer := catalogSealer(t)
	for n := 0; n <= MaxWorkerExecutionsPerType; n++ {
		if n > 0 {
			in.ID, in.Revision = uuid.NewString(), uuid.NewString()
			in.Generation++
			in.Scheduled = time.Now().UTC()
			in.Deadline = in.Scheduled.Add(time.Minute)
			var err error
			in.Payload, err = sealer.Seal(t.Context(), in.Binding(s.nodeID), []byte("check parameters"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
				t.Fatal("retained completed checks consumed active type capacity", n, err)
			}
		}
		poll, err := s.CommitWorkerPoll(t.Context(), a, request)
		if err != nil || len(poll.Offers) != 1 || poll.Offers[0].ExecutionID != in.ID {
			t.Fatal("next generation offer", n, err)
		}
		request.SessionID, request.PollSequence = poll.SessionID, request.PollSequence+1
		start, err := s.CommitWorkerStart(t.Context(), a, workerStartFromOffer(poll))
		if err != nil || start.Disposition != "granted" {
			t.Fatal("next generation start", n, err)
		}
		payload, err := sealer.Seal(t.Context(), secureconfig.Binding{StoreID: s.nodeID, Kind: "WorkerExecution", ID: in.ID, UID: in.MonitorUID, Revision: in.Revision, Purpose: "worker-outcome-test"}, []byte("check successful"))
		if err != nil {
			t.Fatal(err)
		}
		result := WorkerResultInput{ServerID: start.ServerID, ExecutionID: in.ID, ExecutionRevision: in.Revision, GrantID: start.GrantID,
			Status: "success", Digest: identity(in.ID + "success"), Payload: payload}
		if _, err := s.CommitWorkerResult(t.Context(), a, result); err != nil {
			t.Fatal("next generation result", n, err)
		}
	}
	m, _ := s.Get(in.MonitorID)
	if m.TotalChecks != MaxWorkerExecutionsPerType+1 || len(s.fsm.image.WorkerExecutions.Records) != MaxWorkerExecutionsPerType+1 {
		t.Fatal("capacity reuse dropped an observation or retained receipt")
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal("completed receipt inventory cannot be restored", err)
	}
}

func TestWorkerRequeueDifferentWorkerKeepsOldLeaseRejected(t *testing.T) {
	s, first, request, in, principal := workerOfferFixture(t, "recovery")
	poll, err := s.CommitWorkerPoll(t.Context(), first, request)
	if err != nil {
		t.Fatal(err)
	}
	original, _, _ := s.WorkerExecution(t.Context(), in.ID)
	at := original.Lifecycle.OfferDeadline
	if n, err := s.RequeueWorkerExecutions(t.Context(), at, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	other := workerPrincipal("replacement-worker")
	other.Grants = principal.Clone().Grants
	s.administrative = true
	commitWorker(t, s, workerCommand(t, s, "upsert", other))
	s.administrative = false
	second, err := s.AuthenticateWorker(t.Context(), other.TokenSHA256, at.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	request.WorkerID, request.ClientSessionID = other.ID, uuid.NewString()
	next := workerPollAt(t, s, second, request, at.Add(2*time.Millisecond))
	if next.Err != nil || next.WorkerPoll == nil || len(next.WorkerPoll.Offers) != 1 {
		t.Fatal("replacement worker poll", next.Err)
	}
	start := workerStartAt(t, s, second, workerStartFromOffer(*next.WorkerPoll), at.Add(3*time.Millisecond))
	if start.Err != nil || start.WorkerStart == nil || start.WorkerStart.Disposition != "granted" {
		t.Fatal("replacement worker start", start.Err)
	}
	for _, mode := range []string{"begin", "reconcile"} {
		oldRequest := workerStartFromOffer(poll)
		oldRequest.Mode = mode
		response := workerStartAt(t, s, first, oldRequest, at.Add(4*time.Millisecond))
		if response.Err != nil || response.WorkerStart == nil || response.WorkerStart.WorkerUID != first.WorkerUID || response.WorkerStart.Disposition != "rejected" || response.WorkerStart.GrantID != "" || response.WorkerStart.ReceiptID != "" {
			t.Fatal("old worker obtained replacement worker authority", response.Err)
		}
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal("retained original poll cannot survive restart", err)
	}
}

func TestWorkerSchedulingPreservesOfferReservation(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "check")
	if _, err := s.CommitWorkerPoll(t.Context(), a, request); err != nil {
		t.Fatal(err)
	}
	record, _, _ := s.WorkerExecution(t.Context(), in.ID)
	// Format 21 charged serialized record bytes plus the unused portion of a
	// 32 KiB lifecycle slot. Keep those exact bytes valid on replay and restore.
	raw, _ := json.Marshal(record)
	key, _ := json.Marshal(in.ID)
	lease, _ := json.Marshal(record.Lifecycle)
	want := int64(len(key) + len(raw) + 2 + (32 << 10) - len(lease))
	cost, err := workerExecutionRecordCost(in.ID, record)
	if err != nil || cost != want {
		t.Fatal("historical offer reservation changed", cost, want, err)
	}
	if n, err := s.RequeueWorkerExecutions(t.Context(), record.Lifecycle.OfferDeadline, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	retired, _, _ := s.WorkerExecution(t.Context(), in.ID)
	cost, err = workerExecutionRecordCost(in.ID, retired)
	if err != nil || cost != want {
		t.Fatal("retiring an offer required additional capacity", cost, want, err)
	}
}

func TestWorkerStartReorderedTimestampsPreserveCommittedDisposition(t *testing.T) {
	for _, phase := range []string{"offered", "started", "unknown", "rejected", "terminal"} {
		t.Run(phase, func(t *testing.T) {
			s, a, request, in, _ := workerOfferFixture(t, "recovery")
			poll, err := s.CommitWorkerPoll(t.Context(), a, request)
			if err != nil {
				t.Fatal(err)
			}
			r, _, _ := s.WorkerExecution(t.Context(), in.ID)
			start := workerStartFromOffer(poll)
			later := r.Lifecycle.OfferedAt.Add(time.Millisecond)
			want := phase
			switch phase {
			case "offered":
				later = r.Lifecycle.OfferedAt
			case "started", "unknown", "terminal":
				result := workerStartAt(t, s, a, start, later)
				if result.Err != nil || result.WorkerStart == nil || result.WorkerStart.Disposition != "granted" {
					t.Fatal("initial grant", result.Err)
				}
				if phase == "unknown" {
					later = in.Deadline
					result = workerStartAt(t, s, a, start, later)
					if result.Err != nil || result.WorkerStart.Disposition != "unknown" {
						t.Fatal("unknown transition", result.Err)
					}
				}
				if phase == "terminal" {
					later = in.Deadline
					if n, err := s.FinalizeWorkerExecutions(t.Context(), later, 1); err != nil || n != 1 {
						t.Fatal("terminal receipt", n, err)
					}
				}
			case "rejected":
				later = r.Lifecycle.OfferDeadline
				result := workerStartAt(t, s, a, start, later)
				if result.Err != nil || result.WorkerStart.Disposition != "rejected" {
					t.Fatal("initial rejection", result.Err)
				}
			}
			before, _, _ := s.WorkerExecution(t.Context(), in.ID)
			monitor, _ := s.Get(in.MonitorID)
			for _, mode := range []string{"begin", "reconcile"} {
				start.Mode = mode
				result := workerStartAt(t, s, a, start, later.Add(-time.Nanosecond))
				if phase == "offered" {
					if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
						t.Fatal("Start before offer was accepted", result.Err)
					}
					continue
				}
				grant := before.Lifecycle.GrantID
				if phase == "terminal" {
					grant = ""
				}
				if result.Err != nil || result.WorkerStart == nil || result.WorkerStart.Disposition != want || result.WorkerStart.GrantID != grant {
					t.Fatal("reordered duplicate lost the committed disposition", result.Err)
				}
				if receipt, terminal := before.TerminalReceipt(); terminal && (result.WorkerStart.ReceiptID != receipt.ReceiptID || !result.WorkerStart.Deadline.IsZero()) {
					t.Fatal("reordered duplicate lost the terminal receipt")
				}
			}
			after, _, _ := s.WorkerExecution(t.Context(), in.ID)
			current, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(monitor, current) {
				t.Fatal("reordered read changed execution or action state")
			}
		})
	}
}
