//go:build externaljobs

package persistence

import (
	"testing"
	"time"
)

func TestWorkerClosurePreparationFencesFrozenHealthyMessage(t *testing.T) {
	for _, color := range []string{"green", "blue"} {
		t.Run(color, func(t *testing.T) {
			m, closure, recoveryID, at := closureHealthFixture(t, color, WorkerNotificationConditionFormatVersion)
			source := WorkerNotificationSource{Kind: "Monitor", ID: m.ID, UID: "source-uid", Revision: "source-revision"}
			m.Policy.WorkerNotificationSources = map[string][]WorkerNotificationSource{color: {source}}
			in := WorkerExecutionIntent{ID: "frozen-execution", Revision: "frozen-revision", MonitorID: m.ID,
				MonitorUID: m.CatalogUID, MonitorRevision: m.Revision, ActionID: closure.ID, Category: "notification",
				Source: CatalogKey{Kind: "Monitor", ID: source.ID}, SourceUID: source.UID, SourceRevision: source.Revision,
				Scheduled: closure.NotBefore, ExecutionBudget: closure.ExecutionBudget, Notification: NotificationForWorker(closure)}
			f := machine{image: image{Version: WorkerNotificationConditionFormatVersion, Monitors: map[string]Monitor{m.ID: m}}}
			if err := f.checkWorkerActionIdentity(in); err != nil {
				t.Fatal("initial frozen closure identity", err)
			}
			digest := workerExecutionDigest(in)
			c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at.Add(time.Second), Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_missing")}
			r := transitionAtFormat(m, c, WorkerNotificationConditionFormatVersion)
			if r.Err != nil || r.Monitor == nil {
				t.Fatal(r.Err)
			}
			m = *r.Monitor
			f.image.Monitors[m.ID] = m
			if f.checkWorkerActionIdentity(in) == nil || workerExecutionDigest(in) != digest || !m.Actions[closure.ID].Deadline.IsZero() {
				t.Fatal("stale sealed message could start or its identity/budget was rewritten")
			}
			for _, lease := range []*WorkerExecutionLifecycle{nil, {Phase: "offered", OfferDeadline: at.Add(30 * time.Second)}} {
				record := WorkerExecutionRecord{Intent: in, OwnerEpoch: "owner", CreatedAt: at, Lifecycle: lease}
				if reason := f.workerFinalizationReason(record, "owner", at.Add(2*time.Second)); reason != "notification_changed" {
					t.Fatal("queued/offered stale payload did not require safe retirement", reason)
				}
			}
			if recoveryID != "" && !m.Actions[recoveryID].Held() {
				t.Fatal("refresh released uncertain recovery")
			}
		})
	}
}
