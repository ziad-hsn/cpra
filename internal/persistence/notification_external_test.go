//go:build externaljobs

package persistence

import (
	"bytes"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestWorkerCheckNotificationReplayFormats(t *testing.T) {
	for _, version := range []int{WorkerResultFormatVersion, WorkerQueueFormatVersion, NotificationWorkerFormatVersion, DeliveryWorkerFormatVersion} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			s, _, _, input := workerResultFixture(t, "check", "failure")
			record, found, err := s.WorkerExecution(t.Context(), input.ExecutionID)
			if err != nil || !found {
				t.Fatal("worker fixture missing", err)
			}
			fixtureVersion := NotificationFormatVersion
			if version == DeliveryWorkerFormatVersion {
				fixtureVersion = DeliveryFormatVersion
			}
			m, delivered, pending := notificationTransitionFixtureAtFormat(t, Unknown, fixtureVersion)
			s.fsm.image.Monitors[m.ID] = m.Clone()
			// Isolate the observation transition from admission and encryption.
			// The real fixture provides the started grant; protocol contracts test
			// its immutable binding independently.
			record.Intent.MonitorID, record.Intent.MonitorUID = m.ID, m.CatalogUID
			record.Intent.MonitorRevision, record.Intent.ControlRevision = m.Revision, m.ControlRevision
			record.Intent.Generation = m.Generation + 1
			updated, events, applied, noData, held := s.fsm.workerResultTransition(record, input, record.Lifecycle.StartedAt.Add(time.Millisecond), version)
			if !applied || noData || held || updated == nil || !reflect.DeepEqual(updated.Actions[pending.ID], pending) {
				t.Fatal("worker observation changed unfinished delivery")
			}
			if !notificationStorageFormat(version) {
				if _, retained := updated.Actions[delivered.ID]; retained {
					t.Fatal("historical worker replay changed its original actions")
				}
				return
			}
			if !reflect.DeepEqual(updated.Actions[delivered.ID], delivered) {
				t.Fatal("worker observation replaced confirmed delivery")
			}
			for _, e := range events {
				if e.Type == "action_queued" && e.Kind == "code" {
					t.Fatal("worker observation queued another unchanged notification")
				}
			}
		})
	}
}

func TestNotificationWorkerStorageBoundary(t *testing.T) {
	for version := JobTypeFormatVersion; version <= NotificationWorkerFormatVersion; version++ {
		want := max(NotificationFormatVersion, version)
		if externalStorageFormat(version) {
			want = NotificationWorkerFormatVersion
		}
		if joinStorageFormat(NotificationFormatVersion, version) != want || joinStorageFormat(version, NotificationFormatVersion) != want {
			t.Fatal("new observations dropped the worker storage boundary", version)
		}
	}
	s, authority, _, input := workerResultFixture(t, "check", "success")
	c := Command{Kind: "worker_result", At: time.Now().UTC(), commandExtensions: commandExtensions{
		WorkerResult: &WorkerResultCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: authority, Input: input}}}
	if commandWriteFormat(c) != WorkerDispatchFormatVersion || commandMinimumFormat(c) != WorkerResultFormatVersion {
		t.Fatal("worker writer changed the historical replay minimum")
	}
	if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
		t.Fatal(err)
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(workerDispatchSnapshotMagic)) {
		t.Fatal("worker snapshot lost its optional format boundary")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != WorkerDispatchFormatVersion || i.WorkerExecutions == nil {
		t.Fatal("worker snapshot lost execution state")
	}
	i.Version = NotificationFormatVersion
	if validateImageExtensions(i) == nil {
		t.Fatal("common notification snapshot accepted worker state")
	}
}
