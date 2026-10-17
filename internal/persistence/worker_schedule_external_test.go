//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func scheduledWorkerIntent(t *testing.T, s *Store, at time.Time) WorkerExecutionIntent {
	t.Helper()
	in, _ := workerExecutionFixture(t, s, "check")
	in.Scheduled, in.Deadline = at, at.Add(time.Minute)
	ticket, err := s.ScheduleWorkerCheck(at)
	if err != nil {
		t.Fatal(err)
	}
	in.Schedule = &ticket
	return in
}

func commitWorkerSchedule(t *testing.T, s *Store, c *Command) {
	t.Helper()
	if c == nil {
		t.Fatal("missing checkpoint")
	}
	r := submit(t, s, *c)[0]
	if r.Err != nil || !r.Allowed {
		t.Fatal("schedule checkpoint", r.Err)
	}
}

func TestWorkerScheduleHandoffAndOutOfOrderCheckpoints(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	first := scheduledWorkerIntent(t, s, at)
	old := s.WorkerScheduleCheckpoint(time.Now())
	second := scheduledWorkerIntent(t, s, at)
	third, err := s.ScheduleWorkerCheck(at)
	if err != nil {
		t.Fatal(err)
	}
	latest := s.WorkerScheduleCheckpoint(time.Now())
	assert := func() {
		t.Helper()
		r := workerSLOReport(t, s, at)
		if r.Pending != 3 || r.Expected != 0 || r.Samples != 0 || r.Missed != 0 {
			t.Fatal("schedule disappeared or was counted twice", r)
		}
	}
	assert()
	for _, in := range []WorkerExecutionIntent{second, first} {
		record, err := s.CommitWorkerExecution(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		assert() // The owner has not received any local completion yet.
		for _, c := range []*Command{latest, old, latest} {
			commitWorkerSchedule(t, s, c)
			assert()
		}
		replay, err := s.CommitWorkerExecution(t.Context(), in)
		if err != nil || !reflect.DeepEqual(record, replay) {
			t.Fatal("identical admission retry changed the record", err)
		}
		assert()
	}
	if err := s.AbandonWorkerCheck(third); err != nil {
		t.Fatal(err)
	}
	commitWorkerSchedule(t, s, s.WorkerScheduleCheckpoint(time.Now()))
	r := workerSLOReport(t, s, at.Add(12*time.Second))
	if r.Expected != 3 || r.Overdue != 2 || r.Missed != 1 || r.Samples != 0 || r.ResultAttainment == nil || *r.ResultAttainment != 0 {
		t.Fatal("unfinished work improved attainment", r)
	}
	if err := validateWorkerSLO(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerScheduleShutdownKeepsCommittedAssignments(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	in := scheduledWorkerIntent(t, s, at)
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	for range 999 {
		if _, err := s.ScheduleWorkerCheck(at); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.FinishWorkerSchedules(); err != nil {
			t.Fatal(err)
		}
		commitWorkerSchedule(t, s, s.WorkerScheduleCheckpoint(time.Now()))
	}
	r := workerSLOReport(t, s, at)
	if r.Missed != 999 || r.Pending != 1 || r.Expected != 999 {
		t.Fatal("shutdown lost ready work or cancelled admitted work", r)
	}
	record, ok, err := s.WorkerExecution(t.Context(), in.ID)
	if err != nil || !ok || record.Finalization != nil || record.Lifecycle != nil {
		t.Fatal("schedule accounting changed execution authority", err)
	}
}

func TestWorkerScheduleRestartRetainsCountsAndCoverageGap(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Now().UTC()
	for range 3 {
		if _, err := s.ScheduleWorkerCheck(at); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := s.WorkerScheduleCheckpoint(time.Now())
	commitWorkerSchedule(t, s, checkpoint)
	state := s.SLO().Snapshot(time.Now())
	if r := submit(t, s, Command{Kind: "slo", At: time.Now(), SLO: &state})[0]; r.Err != nil {
		t.Fatal(r.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if r := workerSLOReport(t, restored, at.Add(12*time.Second)); r.Expected != 3 || r.Overdue != 3 {
		t.Fatal("restart lost committed waiting obligations", r)
	}
	view := restored.SLOView(time.Now())
	if view.CoverageComplete || view.GapStart.IsZero() || view.GapEnd.IsZero() {
		t.Fatal("restart claimed complete scheduler coverage", view)
	}
	if r := submit(t, restored, *checkpoint)[0]; r.Err == nil {
		t.Fatal("old owner changed recovered schedule counts")
	}
	if _, err := restored.ScheduleWorkerCheck(at); err != nil {
		t.Fatal(err)
	}
	commitWorkerSchedule(t, restored, restored.WorkerScheduleCheckpoint(time.Now()))
	if r := workerSLOReport(t, restored, at.Add(12*time.Second)); r.Expected != 4 || r.Overdue != 4 {
		t.Fatal("new owner reset or reused old obligations", r)
	}
}

func TestWorkerScheduleCheckpointValidationAndSnapshot(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	in := scheduledWorkerIntent(t, s, at)
	c := s.WorkerScheduleCheckpoint(at)
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{*c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion || version == DeliveryWorkerFormatVersion || version == LifecycleWorkerFormatVersion) {
			t.Fatal("format", version, err)
		}
	}
	slot := workerSLOSlot(workerSLOEpoch(at))
	for name, alter := range map[string]func(*Command){
		"mixed":     func(c *Command) { c.MonitorID = "extra" },
		"kind":      func(c *Command) { c.Kind = "barrier" },
		"owner":     func(c *Command) { c.WorkerSchedule.OwnerEpoch = "" },
		"future":    func(c *Command) { c.WorkerSchedule.Buckets[slot].Epoch += 60 },
		"overflow":  func(c *Command) { c.WorkerSchedule.Buckets[slot].Issued = math.MaxUint64 },
		"abandoned": func(c *Command) { c.WorkerSchedule.Buckets[slot].Abandoned = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			command, value := *c, *c.WorkerSchedule
			command.WorkerSchedule = &value
			alter(&command)
			if validateCommand(command) == nil {
				t.Fatal("malformed schedule accepted")
			}
		})
	}
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	commitWorkerSchedule(t, s, c)
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	frozen := *s.fsm.image.WorkerSLO
	if _, err := s.ScheduleWorkerCheck(at); err != nil {
		t.Fatal(err)
	}
	commitWorkerSchedule(t, s, s.WorkerScheduleCheckpoint(time.Now()))
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(lifecycleWorkerSnapshotMagic)) {
		t.Fatal("snapshot does not preserve the delivery worker format")
	}
	if bytes.Contains(sink.Bytes(), []byte("private-execution-parameters-canary")) {
		t.Fatal("snapshot contains plaintext execution parameters")
	}
	saved, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if *saved.WorkerSLO != frozen {
		t.Fatal("snapshot was not frozen")
	}
	for _, alter := range []func(*image){
		func(i *image) { i.Version = WorkerExpiryFormatVersion },
		func(i *image) { i.WorkerSLO.ScheduleOwner = "" },
		func(i *image) { i.WorkerSLO.Buckets[slot].Admitted++ },
		func(i *image) { i.WorkerSLO.Buckets[slot].Admitted = 0 },
		func(i *image) { i.WorkerSLO.Buckets[slot].Scheduled = 0 },
		func(i *image) { i.WorkerSLO.Buckets[slot].Abandoned++ },
	} {
		copy := saved
		copy.imageExtensions = cloneImageExtensions(saved)
		alter(&copy)
		if validateImageExtensions(copy) == nil {
			t.Fatal("corrupt schedule accepted")
		}
	}
}

func TestWorkerScheduleRejectedAdmissionDoesNotMutateCounts(t *testing.T) {
	s := openCatalogMemory(t)
	in := scheduledWorkerIntent(t, s, time.Now().UTC())
	if err := s.AbandonWorkerCheck(*in.Schedule); err != nil {
		t.Fatal(err)
	}
	commitWorkerSchedule(t, s, s.WorkerScheduleCheckpoint(time.Now()))
	before := *s.fsm.image.WorkerSLO
	if _, err := s.CommitWorkerExecution(t.Context(), in); err == nil {
		t.Fatal("abandoned obligation was admitted")
	}
	if *s.fsm.image.WorkerSLO != before {
		t.Fatal("rejected admission mutated measurement")
	}
}

func TestWorkerScheduleCannotOwnTwoAssignments(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	first := scheduledWorkerIntent(t, s, at)
	second := scheduledWorkerIntent(t, s, at)
	if _, err := s.CommitWorkerExecution(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second.Schedule = first.Clone().Schedule
	before := *s.fsm.image.WorkerSLO
	if _, err := s.CommitWorkerExecution(t.Context(), second); err == nil {
		t.Fatal("ticket owned two assignments")
	}
	if *s.fsm.image.WorkerSLO != before {
		t.Fatal("duplicate ticket changed aggregate")
	}
}

func TestWorkerScheduleStorageFailureKeepsUnadmittedDemandVisible(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	in := scheduledWorkerIntent(t, s, at)
	s.MarkUnavailable(errors.New("injected storage failure"))
	if _, err := s.CommitWorkerExecution(t.Context(), in); err == nil {
		t.Fatal("failed storage admitted work")
	}
	r := workerSLOReport(t, s, at.Add(12*time.Second))
	if r.Expected != 1 || r.Overdue != 1 || r.Samples != 0 || r.ResultAttainment == nil || *r.ResultAttainment != 0 {
		t.Fatal("storage failure hid waiting demand", r)
	}
}
