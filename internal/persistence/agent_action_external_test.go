//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestAgentStoragePreservesOptionalFormatBoundary(t *testing.T) {
	if LatestFormatVersion != NotificationWorkerFormatVersion || !supportedFormat(31) || !supportedFormat(32) || !supportedFormat(33) || !supportedFormat(34) || !supportedFormat(35) || !supportedFormat(36) || !supportedFormat(37) || !supportedFormat(38) || !supportedFormat(39) || supportedFormat(40) {
		t.Fatal("agent/worker format boundary changed")
	}
	for version := JobTypeFormatVersion; version <= WorkerScheduleFormatVersion; version++ {
		if joinStorageFormat(31, version) != 32 || joinStorageFormat(version, 31) != 32 {
			t.Fatal("mixed batch could use the common format", version)
		}
	}
	for _, workerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent-first", true: "worker-first"}[workerFirst], func(t *testing.T) {
			s := openCatalogMemory(t)
			if workerFirst {
				commitJobType(t, s, jobTypeFixture(t, s, "test-agent-order"))
			}
			m, a, h := agentActionFixture(t, s, "intervention")
			if !workerFirst {
				if s.fsm.image.Version != NotificationFormatVersion {
					t.Fatal("agent alone enabled optional storage")
				}
				commitJobType(t, s, jobTypeFixture(t, s, "test-agent-order"))
			}
			if err := h.RecordAgentReceipt(t.Context(), agentReceiptCommand(a, 1, "TASK_STATE_SUBMITTED")); err != nil {
				t.Fatal(err)
			}
			if s.fsm.image.Version != NotificationWorkerFormatVersion {
				t.Fatal("optional data was written as a common format")
			}
			i, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			if i.Version != NotificationWorkerFormatVersion || i.JobTypes == nil || i.Monitors[m.ID].Actions[a.ID].AgentReceipt == nil {
				t.Fatal("mixed snapshot lost agent or worker state")
			}
			i.Version = 31
			if validateImageExtensions(i) == nil {
				t.Fatal("optional state accepted in a common snapshot")
			}
		})
	}
}

func TestAgentStorageRejectsWorkerPolicyInCommonEnvelope(t *testing.T) {
	m := testMonitor()
	m.Policy.WorkerNotificationSources = map[string][]WorkerNotificationSource{"red": {{}, {}}, "green": {{}}}
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()}
	if err := validateCommand(c); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{31, 32} {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == 32) {
			t.Fatal("worker policy was confused with common format", version, err)
		}
	}
}

func TestRecoveryNotificationPreservesWorkerFormatBoundary(t *testing.T) {
	for version := JobTypeFormatVersion; version <= WorkerScheduleFormatVersion; version++ {
		if joinStorageFormat(RecoveryNotificationFormatVersion, version) != RecoveryNotificationWorkerFormatVersion ||
			joinStorageFormat(version, RecoveryNotificationFormatVersion) != RecoveryNotificationWorkerFormatVersion {
			t.Fatal("failure notification dropped the optional storage boundary", version)
		}
	}
	if joinStorageFormat(AgentWorkerFormatVersion, RecoveryNotificationFormatVersion) != RecoveryNotificationWorkerFormatVersion {
		t.Fatal("prior combined format was not preserved")
	}
	m := testMonitor()
	m.Policy.Intervention, m.Policy.RecoveryDriver = true, "docker"
	m.Policy.NotificationTriggers = map[string]string{"red": NotifyRecoveryExhausted}
	m.Policy.WorkerNotificationSources = map[string][]WorkerNotificationSource{"red": {{}, {}}, "green": {{}}}
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()}
	if commandWriteFormat(c) != RecoveryNotificationWorkerFormatVersion {
		t.Fatal("new common and optional policy did not select a combined envelope")
	}
	for _, version := range []int{31, 32, 33, 34, 35, 36, 37, 38, 39, 40} {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion || version == WorkerQueueFormatVersion || version == NotificationWorkerFormatVersion) {
			t.Fatal("wrong combined notification format accepted", version, err)
		}
	}
	s := openCatalogMemory(t)
	submit(t, s, c)
	i, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.Version != RecoveryNotificationWorkerFormatVersion || len(i.Monitors[m.ID].Policy.WorkerNotificationSources) != 2 || i.Monitors[m.ID].Policy.NotificationTriggers["red"] != NotifyRecoveryExhausted {
		t.Fatal("combined snapshot lost notification or worker policy")
	}
	i.Version = RecoveryNotificationFormatVersion
	if validateImageExtensions(i) == nil {
		t.Fatal("common format silently accepted optional worker policy")
	}
}

func TestAgentObservationPreservesWorkerFormatBoundary(t *testing.T) {
	for version := JobTypeFormatVersion; version <= AgentObservationWorkerFormatVersion; version++ {
		want := AgentObservationFormatVersion
		if externalStorageFormat(version) {
			want = AgentObservationWorkerFormatVersion
		}
		if joinStorageFormat(AgentObservationFormatVersion, version) != want || joinStorageFormat(version, AgentObservationFormatVersion) != want {
			t.Fatal("observation changed the optional storage boundary", version)
		}
	}
	for _, workerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "observation-first", true: "worker-first"}[workerFirst], func(t *testing.T) {
			s := openCatalogMemory(t)
			if workerFirst {
				commitJobType(t, s, jobTypeFixture(t, s, "test-observation-order"))
			}
			h, action := agentObservationFixture(t, s, "intervention")
			if err := h.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !workerFirst {
				s.fsm.mu.Lock()
				version := s.fsm.image.Version
				s.fsm.mu.Unlock()
				if version != NotificationFormatVersion {
					t.Fatal("agent connection alone enabled optional storage")
				}
				commitJobType(t, s, jobTypeFixture(t, s, "test-observation-order"))
			}
			action, _, _ = s.Action(action.ID)
			if err := s.RecordAgentObservation(t.Context(), action, nextAgentObservation(action, "TASK_STATE_COMPLETED")); err != nil {
				t.Fatal(err)
			}
			image, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			stored := image.Monitors[action.MonitorID].Actions[action.ID]
			if image.Version != NotificationWorkerFormatVersion || image.JobTypes == nil || stored.AgentConnection == nil || stored.AgentReceipt.State != "TASK_STATE_COMPLETED" {
				t.Fatal("combined snapshot lost the connection, observation or optional state")
			}
			image.Version = AgentObservationFormatVersion
			if validateImageExtensions(image) == nil {
				t.Fatal("common snapshot accepted optional state")
			}
		})
	}
}
