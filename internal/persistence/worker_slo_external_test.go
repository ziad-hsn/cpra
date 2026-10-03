//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/slo"
)

func workerSLOReport(t *testing.T, s *Store, at time.Time) slo.Report {
	t.Helper()
	v := s.SLOView(at)
	for _, r := range v.Reports {
		if r.Driver == "external" {
			if v.CoverageComplete || r.QueueAttainment != nil || r.Queue.P99 != nil || r.Execution.P99 != nil {
				t.Fatal("remote grant invented queue or execution timing")
			}
			return r
		}
	}
	t.Fatal("missing external SLO report")
	return slo.Report{}
}

func TestWorkerSLOReceiptTimingAndConcurrentDuplicates(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "check", "success")
	initial := workerSLOReport(t, s, time.Now())
	if initial.Pending != 1 || initial.Samples != 0 || initial.Expected != 0 {
		t.Fatal("admission accounting", initial)
	}
	before := time.Now().UTC()
	receipt, err := s.CommitWorkerResult(t.Context(), a, input)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	if r.Observation == nil || r.Observation.ReceiptID != receipt.ReceiptID ||
		r.Observation.ConfirmedAt.Before(before) || r.Observation.ConfirmedAt.After(after) ||
		r.Observation.ConfirmedAt.Before(r.Result.RecordedAt) || r.Observation.CommittedIndex <= r.Result.CommittedIndex {
		t.Fatal("measurement does not follow the original committed result")
	}
	report := workerSLOReport(t, s, after)
	if report.Samples != 1 || report.Expected != 1 || report.Pending != 0 || report.Missed != 0 || report.Result.P99 == nil ||
		*report.Result.P99 < float64(r.Observation.ConfirmedAt.Sub(r.Intent.Scheduled))/float64(time.Millisecond) {
		t.Fatal("result timing/accounting", report)
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			got, err := s.CommitWorkerResult(t.Context(), a, input)
			if err != nil || got != receipt {
				t.Error("duplicate receipt", err)
			}
			_ = s.SLOView(after)
		})
	}
	group.Wait()
	again, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	if !reflect.DeepEqual(r, again) || !reflect.DeepEqual(report, workerSLOReport(t, s, after)) {
		t.Fatal("duplicates changed original timing or counted another sample")
	}
	if len(s.SLO().View(after, 5*time.Minute, 1000).Reports) != 0 {
		t.Fatal("external delays entered local worker capacity feedback")
	}
}

func TestWorkerSLOHistoricalAdmissionUnavailable(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := workerExecutionFixture(t, s, "check")
	result := workerExecutionSubmitAt(t, s, in, s.executorSession, time.Now().UTC())
	if result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	report := workerSLOReport(t, s, time.Now())
	if report.Condition != "timing_unavailable" || report.ResultAttainment != nil || report.Samples != 0 {
		t.Fatal("historical admission invented a threshold or measurement", report)
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(workerExecutionSnapshotMagic)) {
		t.Fatal("unchanged historical admission was promoted to a newer format")
	}
	frozen, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if frozen.WorkerSLO != nil || frozen.WorkerExecutions.Records[in.ID].ResultTarget != 0 {
		t.Fatal("restore invented a target or window")
	}
}

func commitWorkerResultWithoutObservation(t *testing.T, s *Store, a WorkerAuthority, input WorkerResultInput) WorkerReceipt {
	t.Helper()
	c := WorkerResultCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a, Input: input}
	r := submit(t, s, Command{Kind: "worker_result", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerResult: &c}})[0]
	if r.Err != nil || !r.Allowed || r.WorkerResult == nil {
		t.Fatal("original result", r.Err)
	}
	return *r.WorkerResult
}

func TestWorkerSLOInterruptedMeasurementCannotBeBackfilled(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "check", "success")
	receipt := commitWorkerResultWithoutObservation(t, s, a, input)
	report := workerSLOReport(t, s, time.Now())
	if report.Samples != 0 || report.Expected != 1 || report.Missed != 1 || report.ResultAttainment == nil || *report.ResultAttainment != 0 {
		t.Fatal("missing measurement improved attainment", report)
	}
	for range 3 {
		got, err := s.CommitWorkerResult(t.Context(), a, input)
		if err != nil || got != receipt {
			t.Fatal("lost confirmation damaged the original receipt", err)
		}
	}
	r, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
	if r.Observation != nil || !reflect.DeepEqual(report, workerSLOReport(t, s, time.Now())) {
		t.Fatal("duplicate supplied a new confirmation time")
	}
}

func TestWorkerSLOMissingTimeoutAndActionIsolation(t *testing.T) {
	t.Run("noData", func(t *testing.T) {
		s, a, _, input := workerResultFixture(t, "check", "noData")
		if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
			t.Fatal(err)
		}
		r := workerSLOReport(t, s, time.Now())
		if r.Samples != 0 || r.Expected != 1 || r.Missed != 1 || r.Timeouts != 0 || r.ResultMet != 0 {
			t.Fatal(r)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		s := openCatalogMemory(t)
		in, _ := workerExecutionFixture(t, s, "check")
		in.Deadline = in.Scheduled.Add(10 * time.Second)
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		due := in.Deadline.Add(5 * time.Second)
		pending := workerSLOReport(t, s, due)
		if pending.Overdue != 1 || pending.Expected != 1 || pending.Pending != 0 {
			t.Fatal("unfinished check escaped denominator", pending)
		}
		if n, err := s.FinalizeWorkerExecutions(t.Context(), due, 1); err != nil || n != 1 {
			t.Fatal("timeout finalization", n, err)
		}
		r := workerSLOReport(t, s, due)
		if r.Timeouts != 1 || r.Missed != 1 || r.Expected != 1 || r.Overdue != 0 || r.Samples != 0 || *r.ResultAttainment != 0 {
			t.Fatal("timeout improved attainment", r)
		}
	})
	for category, status := range map[string]string{"recovery": "completed", "notification": "delivered"} {
		t.Run(category, func(t *testing.T) {
			s, a, _, input := workerResultFixture(t, category, status)
			if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
				t.Fatal(err)
			}
			if len(s.SLOView(time.Now()).Reports) != 0 || s.fsm.image.WorkerSLO != nil {
				t.Fatal("long-running action entered health-check SLO")
			}
		})
	}
}

func TestWorkerSLOExactThresholdsAndEstimatedPercentiles(t *testing.T) {
	for _, year := range []int{1900, 2026, 2500} {
		t.Run(strconv.Itoa(year), func(t *testing.T) {
			scheduled := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			f := &machine{image: image{LocalExecutorSession: "owner", imageExtensions: imageExtensions{
				WorkerExecutions: &workerExecutionImage{Records: map[string]WorkerExecutionRecord{}}}}}
			for n := range 1000 {
				latency := time.Millisecond
				if n >= 500 {
					latency = 250 * time.Millisecond
				}
				if n >= 950 {
					latency = 5 * time.Second
				}
				if n >= 990 {
					latency = 5*time.Second + time.Nanosecond
				}
				index := uint64(n*3 + 1)
				id := strconv.Itoa(n)
				r := WorkerExecutionRecord{ResultTarget: 5 * time.Second, OwnerEpoch: "owner", CommittedIndex: index,
					Intent: WorkerExecutionIntent{ID: id, Revision: "revision", Category: "check", Scheduled: scheduled},
					Result: &WorkerResultRecord{Applied: true, RecordedAt: scheduled, CommittedIndex: index + 1, Receipt: WorkerReceipt{ReceiptID: "receipt-" + id}}}
				if err := f.admitWorkerSLO(&r, scheduled); err != nil {
					t.Fatal(err)
				}
				f.finishWorkerSLO(r, scheduled)
				f.image.WorkerExecutions.Records[id] = r
				command := WorkerObservationCommand{ExecutionID: id, ExecutionRevision: "revision", ReceiptID: r.Result.Receipt.ReceiptID, OwnerEpoch: "owner"}
				at := scheduled.Add(latency)
				result := f.applyWorkerObservation(command, at, index+2)
				if result.Err != nil || !result.Allowed {
					t.Fatal(result.Err)
				}
				// Even a later, contradictory timestamp cannot rewrite first confirmation.
				result = f.applyWorkerObservation(command, at.Add(time.Minute), index+3)
				if result.Err != nil || !result.Allowed {
					t.Fatal(result.Err)
				}
				f.image.Index = index + 3
			}
			r := f.image.WorkerSLO.report(scheduled.Add(10*time.Second), 1000)
			if r.Samples != 1000 || r.Expected != 1000 || r.ResultMet != 990 || *r.ResultAttainment != .99 ||
				*r.Result.P50 != 1 || *r.Result.P95 != 250 || *r.Result.P99 != 5000 || r.QueueAttainment != nil {
				t.Fatal("threshold/distribution oracle", r)
			}
			if err := validateWorkerSLO(f.image); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerSLOTargetChangesAndExpiredSlots(t *testing.T) {
	for _, mode := range []string{"target", "slot"} {
		t.Run(mode, func(t *testing.T) {
			s, a, _, input := workerResultFixture(t, "check", "success")
			original, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			at := time.Now().UTC()
			for _, target := range []time.Duration{10 * time.Second, 5 * time.Second} {
				in, _ := workerExecutionFixture(t, s, "check")
				at = time.Now().UTC()
				if mode == "slot" {
					in.Scheduled = original.Intent.Scheduled.Add(5 * time.Minute)
					at = in.Scheduled
					target = 5 * time.Second
				}
				cmd := WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession, ResultTarget: target}
				if result := submit(t, s, Command{Kind: "worker_execution", At: at, commandExtensions: commandExtensions{WorkerExecution: &cmd}})[0]; result.Err != nil || !result.Allowed {
					t.Fatal("admission", result.Err)
				}
				if mode == "slot" {
					break
				}
			}
			before := *s.fsm.image.WorkerSLO
			if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
				t.Fatal(err)
			}
			if before != *s.fsm.image.WorkerSLO {
				t.Fatal("old result contaminated a new target or newer slot")
			}
			if got := workerSLOReport(t, s, at); got.Pending != 1 || got.Samples != 0 {
				t.Fatal("new-window accounting", got)
			}
		})
	}
}

func TestWorkerSLOTargetChangesWithinOneCommittedBatch(t *testing.T) {
	s := openCatalogMemory(t)
	var commands []Command
	for _, target := range []time.Duration{5 * time.Second, 10 * time.Second, 5 * time.Second} {
		in, _ := workerExecutionFixture(t, s, "check")
		c := WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession, ResultTarget: target}
		commands = append(commands, Command{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &c}})
	}
	results := submit(t, s, commands...)
	var epoch uint64
	for _, result := range results {
		if result.Err != nil || !result.Allowed || result.WorkerExecution == nil || result.WorkerExecution.SLOEpoch <= epoch {
			t.Fatal("target reused a window identity", result.Err)
		}
		epoch = result.WorkerExecution.SLOEpoch
	}
	if r := workerSLOReport(t, s, time.Now()); r.Pending != 1 || r.Samples != 0 {
		t.Fatal("earlier same-target work entered a different window", r)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSLOSnapshotIsolationAndCorruption(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "check", "success")
	if _, err := s.CommitWorkerResult(t.Context(), a, input); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	before := *s.fsm.image.WorkerSLO
	in, _ := workerExecutionFixture(t, s, "check")
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(notificationWorkerSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("canary")) {
		t.Fatal("format or plaintext")
	}
	frozen, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if frozen.WorkerSLO == nil || *frozen.WorkerSLO != before {
		t.Fatal("background snapshot saw later updates")
	}
	record := frozen.WorkerExecutions.Records[input.ExecutionID]
	slot := workerSLOSlot(workerSLOEpoch(record.Intent.Scheduled))
	for name, corrupt := range map[string]func(*image){
		"format":         func(i *image) { i.Version = WorkerFinalizationFormatVersion },
		"missing-window": func(i *image) { i.WorkerSLO = nil },
		"missing-epoch":  func(i *image) { i.WorkerSLO.Epoch = 0 },
		"target":         func(i *image) { i.WorkerSLO.ResultTarget = -1 },
		"counts":         func(i *image) { i.WorkerSLO.Buckets[slot].Completed++ },
		"threshold":      func(i *image) { i.WorkerSLO.Buckets[slot].ResultMet = 2 },
		"overflow":       func(i *image) { i.WorkerSLO.Buckets[slot].Result.Buckets[0] = math.MaxUint64 },
		"receipt":        func(i *image) { i.WorkerExecutions.Records[input.ExecutionID].Observation.ReceiptID = "other" },
		"early": func(i *image) {
			i.WorkerExecutions.Records[input.ExecutionID].Observation.ConfirmedAt = record.Result.RecordedAt.Add(-time.Nanosecond)
		},
		"index": func(i *image) {
			i.WorkerExecutions.Records[input.ExecutionID].Observation.CommittedIndex = record.Result.CommittedIndex
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := frozen
			copy.imageExtensions = cloneImageExtensions(frozen)
			corrupt(&copy)
			if validateImageExtensions(copy) == nil {
				t.Fatal("corrupt measurement accepted")
			}
		})
	}
}

func TestWorkerSLOCommandFormatsAndAuthority(t *testing.T) {
	s, a, _, input := workerResultFixture(t, "check", "success")
	receipt := commitWorkerResultWithoutObservation(t, s, a, input)
	c := WorkerObservationCommand{ExecutionID: input.ExecutionID, ExecutionRevision: input.ExecutionRevision, ReceiptID: receipt.ReceiptID, OwnerEpoch: s.executorSession}
	command := Command{Kind: "worker_observation", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerObservation: &c}}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == WorkerSLOFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerScheduleFormatVersion || version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion) {
			t.Fatal("format", version, err)
		}
	}
	for name, alter := range map[string]func(*Command){
		"mixed":   func(c *Command) { c.MonitorID = "unrelated" },
		"kind":    func(c *Command) { c.Kind = "worker_result" },
		"nil":     func(c *Command) { c.WorkerObservation = nil },
		"receipt": func(c *Command) { c.WorkerObservation.ReceiptID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			copy, value := command, c
			copy.WorkerObservation = &value
			alter(&copy)
			if validateCommand(copy) == nil {
				t.Fatal("malformed observation accepted")
			}
		})
	}
	for _, bad := range []WorkerObservationCommand{
		{ExecutionID: c.ExecutionID, ExecutionRevision: c.ExecutionRevision, ReceiptID: "wrong", OwnerEpoch: c.OwnerEpoch},
		{ExecutionID: c.ExecutionID, ExecutionRevision: c.ExecutionRevision, ReceiptID: c.ReceiptID, OwnerEpoch: "previous-owner"},
	} {
		copy := command
		copy.WorkerObservation = &bad
		if got := submit(t, s, copy)[0]; got.Err == nil || got.Allowed {
			t.Fatal("unbound timing accepted")
		}
	}
	if result := submit(t, s, command)[0]; result.Err != nil || !result.Allowed {
		t.Fatal("observation", result.Err)
	}
	if got := workerSLOReport(t, s, time.Now()); got.Samples != 1 || got.Missed != 0 {
		t.Fatal(got)
	}
}
