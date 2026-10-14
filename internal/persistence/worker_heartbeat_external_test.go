//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func heartbeatFromResult(start WorkerStartRequest, in WorkerResultInput) WorkerHeartbeatRequest {
	return WorkerHeartbeatRequest{ServerID: start.ServerID, SessionID: start.SessionID, ExecutionID: in.ExecutionID, GrantID: in.GrantID}
}

func TestWorkerHeartbeatKeepsOriginalGrantAndDeadline(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			status := "unknown"
			if category == "check" {
				status = "noData"
			}
			s, a, start, input := workerResultFixture(t, category, status)
			before, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			request := heartbeatFromResult(start, input)
			for n := 0; n < 3; n++ {
				response, err := s.CommitWorkerHeartbeat(t.Context(), a, request)
				if err != nil || !response.Accepted || response.WorkerHeartbeatRequest != request || response.WorkerUID != a.WorkerUID {
					t.Fatal("heartbeat", response, err)
				}
			}
			saved, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			if saved.Intent.Deadline != before.Intent.Deadline || saved.Lifecycle.GrantID != before.Lifecycle.GrantID || saved.Lifecycle.StartedAt != before.Lifecycle.StartedAt || saved.Lifecycle.Heartbeat == nil {
				t.Fatal("heartbeat renewed execution permission")
			}
			// No fresh poll is necessary while all capacity is occupied. Evaluate
			// using an explicit command timestamp after the polling session expires.
			session := s.fsm.image.WorkerSessions.Sessions[a.WorkerUID]
			at := session.Response.SessionExpiresAt.Add(time.Second)
			c := WorkerHeartbeatCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request}
			result := submit(t, s, Command{Kind: "worker_heartbeat", At: at, commandExtensions: commandExtensions{WorkerHeartbeat: &c}})[0]
			if result.Err != nil || result.WorkerHeartbeat == nil || !result.WorkerHeartbeat.Accepted {
				t.Fatal("idle poll session stopped active handler", result.Err)
			}
			at = before.Intent.Deadline
			result = submit(t, s, Command{Kind: "worker_heartbeat", At: at, commandExtensions: commandExtensions{WorkerHeartbeat: &c}})[0]
			if result.Err != nil || result.WorkerHeartbeat == nil || result.WorkerHeartbeat.Accepted {
				t.Fatal("expired execution continued", result.Err)
			}
			if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerHeartbeatControlsDoNotConflateAcknowledgementAndDismissal(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, start, input := workerResultFixture(t, category, "unknown")
			request := heartbeatFromResult(start, input)
			r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			m, _ := s.Get(r.Intent.MonitorID)
			applyControlRequest(t, s, controlRequest(m, "acknowledge", uuid.NewString(), time.Now().UTC()))
			v, err := s.CommitWorkerHeartbeat(t.Context(), a, request)
			if err != nil || !v.Accepted {
				t.Fatal("acknowledge stopped handler", err)
			}
			m, _ = s.Get(m.ID)
			applyControlRequest(t, s, controlRequest(m, "dismiss", uuid.NewString(), time.Now().UTC()))
			v, err = s.CommitWorkerHeartbeat(t.Context(), a, request)
			if err != nil || v.Accepted != (category == "recovery") {
				t.Fatal("dismissal affected wrong category", v, err)
			}
			m, _ = s.Get(m.ID)
			applyControlRequest(t, s, controlRequest(m, "reopen", uuid.NewString(), time.Now().UTC()))
			v, err = s.CommitWorkerHeartbeat(t.Context(), a, request)
			if err != nil || v.Accepted != (category == "recovery") {
				t.Fatal("reopen revived a canceled grant", v, err)
			}
		})
	}
}

func TestWorkerHeartbeatSnoozeStopsWithoutLosingOutcome(t *testing.T) {
	s, a, start, input := workerResultFixture(t, "recovery", "completed")
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	m, _ := s.Get(r.Intent.MonitorID)
	applyControlRequest(t, s, controlRequest(m, "snooze", uuid.NewString(), time.Now().UTC()))
	v, err := s.CommitWorkerHeartbeat(t.Context(), a, heartbeatFromResult(start, input))
	if err != nil || v.Accepted {
		t.Fatal("snoozed handler continued", err)
	}
	// Cooperative cancellation cannot prove the handler stopped before its effect.
	receipt, err := s.CommitWorkerResult(t.Context(), a, input)
	if err != nil || receipt.Disposition != "accepted" {
		t.Fatal("known completion lost after stop request", err)
	}
	before, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	v, err = s.CommitWorkerHeartbeat(t.Context(), a, heartbeatFromResult(start, input))
	after, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil || v.Accepted || !reflect.DeepEqual(before, after) {
		t.Fatal("terminal heartbeat changed original result", err)
	}
}

func TestWorkerHeartbeatIdentityAndInterruptedOwner(t *testing.T) {
	s, a, start, input := workerResultFixture(t, "check", "success")
	request := heartbeatFromResult(start, input)
	for _, change := range []func(*WorkerAuthority, *WorkerHeartbeatRequest){
		func(a *WorkerAuthority, _ *WorkerHeartbeatRequest) { a.WorkerUID = uuid.NewString() },
		func(a *WorkerAuthority, _ *WorkerHeartbeatRequest) { a.CredentialRevision = uuid.NewString() },
		func(_ *WorkerAuthority, r *WorkerHeartbeatRequest) { r.SessionID = uuid.NewString() },
		func(_ *WorkerAuthority, r *WorkerHeartbeatRequest) { r.GrantID = uuid.NewString() },
		func(_ *WorkerAuthority, r *WorkerHeartbeatRequest) { r.ExecutionID = uuid.NewString() },
	} {
		x, y := a, request
		change(&x, &y)
		if _, err := s.CommitWorkerHeartbeat(t.Context(), x, y); err == nil {
			t.Fatal("foreign heartbeat accepted")
		}
	}
	// A replacement controller owner must never renew the former owner's grant.
	s.fsm.mu.Lock()
	s.fsm.image.LocalExecutorSession = uuid.NewString()
	s.executorSession = s.fsm.image.LocalExecutorSession
	s.fsm.mu.Unlock()
	v, err := s.CommitWorkerHeartbeat(t.Context(), a, request)
	if err != nil || v.Accepted {
		t.Fatal("former owner continued", err)
	}
	receipt, err := s.CommitWorkerResult(t.Context(), a, input)
	if err != nil || receipt.Disposition != "finalized" {
		t.Fatal("old check became fresh health", err)
	}
}

func TestWorkerHeartbeatSnapshotAndCommandFormat(t *testing.T) {
	s, a, start, input := workerResultFixture(t, "check", "success")
	request := heartbeatFromResult(start, input)
	if _, err := s.CommitWorkerHeartbeat(t.Context(), a, request); err != nil {
		t.Fatal(err)
	}
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	frozen, _, err := s.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitWorkerHeartbeat(t.Context(), a, request); err != nil {
		t.Fatal(err)
	}
	sink := &collectionTestSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != DeliveryWorkerFormatVersion {
		t.Fatalf("heartbeat snapshot format: got %d, want %d", i.Version, DeliveryWorkerFormatVersion)
	}
	if err := validateWorkerExecutionImage(i); err != nil {
		t.Fatal("heartbeat snapshot invalid", err)
	}
	state := i.WorkerExecutions.Records[input.ExecutionID]
	if !reflect.DeepEqual(state.Lifecycle.Heartbeat, frozen.Lifecycle.Heartbeat) {
		t.Fatal("later heartbeat changed frozen snapshot")
	}
	state.Lifecycle.Heartbeat.At = state.Intent.Deadline.Add(time.Second)
	i.WorkerExecutions.Records[input.ExecutionID] = state
	if validateWorkerExecutionImage(i) == nil {
		t.Fatal("out-of-deadline acceptance survived restore")
	}
	c := Command{Kind: "worker_heartbeat", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerHeartbeat: &WorkerHeartbeatCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Request: request}}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerLifecycleFormatVersion || version == WorkerFinalizationFormatVersion || version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion) {
			t.Fatal("heartbeat format", version, err)
		}
	}
	c.MonitorID = "unrelated"
	if validateCommand(c) == nil {
		t.Fatal("mixed command admitted")
	}
	c.MonitorID = ""
	c.WorkerHeartbeat.Request.GrantID = strings.Repeat("x", 257)
	if !errors.Is(validateCommand(c), ErrWorkerExecutionInvalid) {
		t.Fatal("oversized identity admitted")
	}
}
