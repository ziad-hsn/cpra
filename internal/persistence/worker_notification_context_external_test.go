//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestWorkerNotificationContextIsBoundAndRestored(t *testing.T) {
	s := openCatalogMemory(t)
	in, _ := inlineNotificationFixture(t, s)
	m, _ := s.Get(in.MonitorID)
	in.Notification = NotificationForWorker(m.Actions[in.ActionID])
	for _, change := range []func(*WorkerNotification){
		func(n *WorkerNotification) { n.Color = "green" },
		func(n *WorkerNotification) { n.CorrelationID = "different-condition" },
		func(n *WorkerNotification) { n.At = n.At.Add(time.Second) },
	} {
		bad := in.Clone()
		change(bad.Notification)
		if _, err := s.CommitWorkerExecution(t.Context(), bad); err == nil {
			t.Fatal("altered notification was admitted")
		}
	}
	record, err := s.CommitWorkerExecution(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{Kind: "worker_execution", At: in.Scheduled, commandExtensions: commandExtensions{WorkerExecution: &WorkerExecutionCommand{Intent: in, OwnerEpoch: s.executorSession}}}
	for _, format := range []int{43, 45} {
		raw, _ := json.Marshal(envelope{Version: format, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (format == 45) {
			t.Fatal("notification context format gate", format, err)
		}
	}
	if err = s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	restored, ok, err := s.WorkerExecution(t.Context(), in.ID)
	if err != nil || !ok || !reflect.DeepEqual(record.Intent.Notification, restored.Intent.Notification) {
		t.Fatal("snapshot changed notification meaning", err)
	}
}

func TestWorkerBlueChangedMessageRetiresWithoutFreshBudget(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	opening := notificationByColor(t, m, "blue", Queued)
	m = policyStart(t, m, opening, at.Add(time.Millisecond))
	m = policyOutcome(t, m, opening, at.Add(2*time.Millisecond), "success", false)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	old := notificationByColor(t, m, "blue", Queued).Clone()
	in := WorkerExecutionIntent{MonitorID: m.ID, MonitorRevision: m.Revision, ActionID: old.ID, Category: "notification", Notification: NotificationForWorker(old), Deadline: old.Deadline}
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	f := &machine{image: image{Monitors: map[string]Monitor{m.ID: m}}}
	for _, lease := range []*WorkerExecutionLifecycle{nil, {Phase: "offered"}, {Phase: "offered", OfferDeadline: at}} {
		if reason := f.workerFinalizationReason(WorkerExecutionRecord{Intent: in, Lifecycle: lease, CreatedAt: at, OwnerEpoch: "owner"}, "owner", at.Add(4*time.Second)); reason != "notification_changed" {
			t.Fatal("ungranted stale message must retire before deadline", reason)
		}
	}
	next, _ := f.workerFinalizationTransition(WorkerExecutionRecord{Intent: in, Lifecycle: &WorkerExecutionLifecycle{Phase: "rejected"}}, at.Add(4*time.Second))
	if next == nil || next.Actions[old.ID].Outcome != "worker_notification_changed" {
		t.Fatal("stale worker message was not retired")
	}
	alert := next.Notifications.ReviewAlerts[old.ReviewNotice.AlertID]
	replacement := next.Actions[alert.ClosingDelivery]
	if replacement.ID == old.ID || replacement.State != Queued || replacement.ReviewNotice.Health != "unhealthy" || !replacement.Deadline.Equal(old.Deadline) || replacement.NotificationCorrelation != old.NotificationCorrelation {
		t.Fatal("replacement lost current wording, original correlation or deadline")
	}
	f.image.Monitors[m.ID] = m
	expired, _ := f.workerFinalizationTransition(WorkerExecutionRecord{Intent: in}, old.Deadline)
	if expired.Actions[old.ID].Outcome != "notification_deadline" || expired.Notifications.ReviewAlerts[old.ReviewNotice.AlertID].ClosingDelivery != old.ID {
		t.Fatal("expired delivery acquired new authorization")
	}
}
