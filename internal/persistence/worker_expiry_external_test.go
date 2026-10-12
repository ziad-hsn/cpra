//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func workerExpiryFixture(t *testing.T, s *Store, category string) WorkerExecutionIntent {
	t.Helper()
	var in WorkerExecutionIntent
	if category == "inline-notification" {
		in, _ = inlineNotificationFixture(t, s)
	} else {
		in, _ = workerExecutionFixture(t, s, category)
	}
	in.Deadline = in.Scheduled.Add(time.Nanosecond)
	return in
}

func workerExpiryEvents(t *testing.T, s *Store, id string) []Event {
	t.Helper()
	page, err := s.History().Page(id, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var found []Event
	for _, e := range page.Events {
		if e.Outcome == "worker_admission_expired" {
			found = append(found, e)
		}
	}
	return found
}

func TestWorkerExpiryCancelsOnlyTheOriginalUnstartedAction(t *testing.T) {
	for _, category := range []string{"recovery", "notification", "inline-notification"} {
		t.Run(category, func(t *testing.T) {
			s := openCatalogMemory(t)
			in := workerExpiryFixture(t, s, category)
			before, _ := s.Get(in.MonitorID)
			if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
				t.Fatal(err)
			}
			after, _ := s.Get(in.MonitorID)
			a := after.Actions[in.ActionID]
			if a.State != Cancelled || a.Outcome != "worker_admission_expired" || !a.StartedAt.IsZero() ||
				a.ExecutorKind != "" || a.ExecutorSession != "" || a.FinishedAt.Before(in.Deadline) {
				t.Fatal("expiry invented an invocation or lost its disposition", a)
			}
			comparison := after.Clone()
			comparison.Actions[in.ActionID] = before.Actions[in.ActionID]
			if !reflect.DeepEqual(before, comparison) {
				t.Fatal("expiry changed another endpoint, health, counters, cooldown or incident")
			}
			for range 3 {
				if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
					t.Fatal(err)
				}
			}
			again, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(after, again) || s.fsm.image.WorkerExecutions != nil {
				t.Fatal("retry changed the disposition or allocated an execution")
			}
			events := workerExpiryEvents(t, s, in.MonitorID)
			if len(events) != 1 || events[0].ActionID != a.ID || events[0].Type != "action_cancelled" || events[0].IncidentID != a.IncidentID || !events[0].At.Equal(a.FinishedAt) {
				t.Fatal("missing or duplicate original expiry event", events)
			}
			if category == "recovery" {
				// Expiry must not manufacture a confirmed recovery failure and thereby
				// unlock another attempt on the next failed check.
				result := submit(t, s, Command{Kind: "pulse", MonitorID: in.MonitorID, Revision: in.MonitorRevision,
					Generation: before.Generation + 1, Outcome: "failure", At: time.Now().UTC(), Guard: &in.Guard})[0]
				if result.Err != nil || result.Monitor == nil {
					t.Fatal("failed observation was not committed", result.Err)
				}
				m, _ := s.Get(in.MonitorID)
				if m.InterventionAttempts != before.InterventionAttempts || m.pendingIntervention() || !m.Incident {
					t.Fatal("expiry unlocked recovery or hid the incident")
				}
			}
		})
	}
}

func TestWorkerExpiryRejectsStaleConfigurationAndActionIdentity(t *testing.T) {
	s := openCatalogMemory(t)
	in := workerExpiryFixture(t, s, "inline-notification")
	before, _ := s.Get(in.MonitorID)
	for name, change := range map[string]func(*WorkerExecutionIntent){
		"incarnation": func(i *WorkerExecutionIntent) { i.MonitorUID = uuid.NewString() },
		"revision":    func(i *WorkerExecutionIntent) { i.MonitorRevision = uuid.NewString() },
		"control":     func(i *WorkerExecutionIntent) { i.ControlRevision = uuid.NewString() },
		"action":      func(i *WorkerExecutionIntent) { i.ActionID = uuid.NewString() },
		"source":      func(i *WorkerExecutionIntent) { i.SourceRevision = uuid.NewString() },
		"job-type":    func(i *WorkerExecutionIntent) { i.JobType.Revision = uuid.NewString() },
		"schedule":    func(i *WorkerExecutionIntent) { i.Scheduled = i.Scheduled.Add(-time.Second) },
		"early":       func(i *WorkerExecutionIntent) { i.Deadline = i.Scheduled.Add(time.Hour) },
		"category":    func(i *WorkerExecutionIntent) { i.Category = "recovery" },
		"generation":  func(i *WorkerExecutionIntent) { i.Generation = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in.Clone()
			change(&bad)
			if err := s.ExpireWorkerAction(t.Context(), bad); err == nil {
				t.Fatal("stale or mismatched action expired")
			}
			after, _ := s.Get(in.MonitorID)
			if !reflect.DeepEqual(before, after) || len(workerExpiryEvents(t, s, in.MonitorID)) != 0 {
				t.Fatal("rejected expiry changed the monitor")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.ExpireWorkerAction(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal("expiry ignored cancellation", err)
	}
	c := workerExpiryCommand(in, "previous-owner")
	result := submit(t, s, Command{Kind: "worker_expire", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExpire: &c}})[0]
	if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
		t.Fatal("previous owner expired an action", result.Err)
	}
}

func TestWorkerExpiryPreservesEveryAdmittedPhase(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		for _, phase := range []string{"queued", "offered", "started", "unknown"} {
			t.Run(category+"/"+phase, func(t *testing.T) {
				s, authority, pollRequest, in, _ := workerOfferFixture(t, category)
				if phase != "queued" {
					poll, err := s.CommitWorkerPoll(t.Context(), authority, pollRequest)
					if err != nil {
						t.Fatal(err)
					}
					if phase != "offered" {
						if _, err := s.CommitWorkerStart(t.Context(), authority, workerStartFromOffer(poll)); err != nil {
							t.Fatal(err)
						}
						if phase == "unknown" {
							if _, err := s.FinalizeWorkerExecutions(t.Context(), in.Deadline, 1); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				before, _ := s.Get(in.MonitorID)
				record, _, _ := s.WorkerExecution(t.Context(), in.ID)
				c := workerExpiryCommand(in, s.executorSession)
				result := submit(t, s, Command{Kind: "worker_expire", At: in.Deadline, commandExtensions: commandExtensions{WorkerExpire: &c}})[0]
				if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
					t.Fatal("expiry took ownership of an admitted action", result.Err)
				}
				after, _ := s.Get(in.MonitorID)
				again, _, _ := s.WorkerExecution(t.Context(), in.ID)
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(record, again) || len(workerExpiryEvents(t, s, in.MonitorID)) != 0 {
					t.Fatal("expiry changed admitted work")
				}
			})
		}
	}
}

func TestWorkerExpiryPreservesCommittedResult(t *testing.T) {
	for category, status := range map[string]string{"recovery": "completed", "notification": "delivered"} {
		t.Run(category, func(t *testing.T) {
			s, authority, _, input := workerResultFixture(t, category, status)
			if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
				t.Fatal(err)
			}
			r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			before, _ := s.Get(r.Intent.MonitorID)
			c := workerExpiryCommand(r.Intent, s.executorSession)
			result := submit(t, s, Command{Kind: "worker_expire", At: r.Intent.Deadline, commandExtensions: commandExtensions{WorkerExpire: &c}})[0]
			if !errors.Is(result.Err, ErrWorkerExecutionConflict) {
				t.Fatal("expiry replaced a completed result", result.Err)
			}
			after, _ := s.Get(r.Intent.MonitorID)
			again, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(r, again) {
				t.Fatal("expiry changed the original result")
			}
		})
	}
}

func TestWorkerExpiryAndAdmissionHaveOneWinner(t *testing.T) {
	for range 8 {
		s := openCatalogMemory(t)
		in, _ := workerExecutionFixture(t, s, "recovery")
		c := workerExpiryCommand(in, s.executorSession)
		var expiry Result
		var admissionErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			expiry = submit(t, s, Command{Kind: "worker_expire", At: in.Deadline, commandExtensions: commandExtensions{WorkerExpire: &c}})[0]
		})
		wg.Go(func() { _, admissionErr = s.CommitWorkerExecution(t.Context(), in) })
		wg.Wait()
		m, _ := s.Get(in.MonitorID)
		_, admitted, err := s.WorkerExecution(t.Context(), in.ID)
		if err != nil || (expiry.Err == nil) == (admissionErr == nil) || admitted != (admissionErr == nil) ||
			(m.Actions[in.ActionID].State == Cancelled) == admitted {
			t.Fatal("admission and expiry did not preserve a single winner", expiry.Err, admissionErr, err)
		}
	}
}

func TestWorkerExpiryDoesNotRequireExecutionCapacity(t *testing.T) {
	s := openCatalogMemory(t)
	in := workerExpiryFixture(t, s, "recovery")
	// This fixture fills the charged byte budget without allocating payloads.
	s.fsm.mu.Lock()
	s.fsm.image.WorkerExecutions = &workerExecutionImage{Records: map[string]WorkerExecutionRecord{}, EncodedBytes: MaxWorkerExecutionBytes}
	s.fsm.mu.Unlock()
	if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
		t.Fatal("full execution capacity prevented expiry", err)
	}
	if s.fsm.image.WorkerExecutions.EncodedBytes != MaxWorkerExecutionBytes || len(s.fsm.image.WorkerExecutions.Records) != 0 {
		t.Fatal("expiry consumed execution capacity")
	}
}

func TestWorkerExpiryCommandAndSnapshotValidation(t *testing.T) {
	s := openCatalogMemory(t)
	in := workerExpiryFixture(t, s, "recovery")
	c := workerExpiryCommand(in, s.executorSession)
	command := Command{Kind: "worker_expire", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExpire: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion) {
			t.Fatal("expiry format boundary", version, err)
		}
	}
	for name, change := range map[string]func(*Command){
		"missing":          func(c *Command) { c.WorkerExpire = nil },
		"mixed":            func(c *Command) { c.Generation = 1 },
		"different-kind":   func(c *Command) { c.Kind = "barrier" },
		"unbounded-id":     func(c *Command) { c.WorkerExpire.MonitorID = strings.Repeat("x", 257) },
		"unbounded-window": func(c *Command) { c.WorkerExpire.Deadline = c.WorkerExpire.Scheduled.Add(25 * time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			copy, value := command, c
			copy.WorkerExpire = &value
			change(&copy)
			if validateCommand(copy) == nil {
				t.Fatal("malformed expiry accepted")
			}
		})
	}
	if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(notificationWorkerSnapshotMagic)) || bytes.Contains(raw, []byte("private-execution-parameters-canary")) {
		t.Fatal("snapshot format or confidentiality")
	}
	saved, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if saved.Monitors[in.MonitorID].Actions[in.ActionID].Outcome != "worker_admission_expired" {
		t.Fatal("snapshot lost expiry")
	}
	saved.Version = WorkerNotificationFormatVersion
	if validateImageExtensions(saved) == nil {
		t.Fatal("snapshot downgrade accepted")
	}
	saved.Version = WorkerExpiryFormatVersion
	m := saved.Monitors[in.MonitorID]
	a := m.Actions[in.ActionID]
	a.StartedAt = a.FinishedAt
	m.Actions[a.ID] = a
	saved.Monitors[m.ID] = m
	if validateImageExtensions(saved) == nil {
		t.Fatal("snapshot turned expiry into an invocation")
	}
}
