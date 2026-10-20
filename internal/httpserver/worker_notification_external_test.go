//go:build externaljobs

package httpserver

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerNotificationProductionReceiptAndUnknown(t *testing.T) {
	for _, status := range []string{"delivered", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := newWorkerSessionFixtureWithSource(t, "notification", "Monitor")
			record := queueWorkerHTTPActionSource(t, f, "notification", true)
			_, client, _ := startWorkerProductionServer(t, f, true, nil)
			offers, err := client.Poll(t.Context(), f.request)
			if err != nil || offers == nil || len(offers.Items) != 1 {
				t.Fatal("inline notification offer", err)
			}
			a := offers.Items[0]
			if a.ExecutionID != record.Intent.ID || a.IncarnationUID != record.Intent.MonitorUID || a.CredentialProfile != "worker-local-profile" || !bytes.Contains(a.Parameters, []byte("9007199254740993")) {
				t.Fatal("inline assignment lost original identity or parameters")
			}
			start, err := client.Start(t.Context(), startWorkerOffer(a, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("inline notification start", err)
			}
			input := api.Outcome{ServerID: a.ServerID, WorkerUID: a.WorkerUID, ExecutionID: a.ExecutionID, GrantID: start.GrantID, Kind: "notification", Status: status, Evidence: []string{}, Data: json.RawMessage(`"private-schema-canary"`)}
			receipt, err := client.Result(t.Context(), input)
			if err != nil {
				t.Fatal("inline notification result", err)
			}
			again, err := client.Result(t.Context(), input)
			if err != nil || !reflect.DeepEqual(receipt, again) {
				t.Fatal("repeated result changed original receipt", err)
			}
			for _, mode := range []api.StartMode{api.StartModeBegin, api.StartModeReconcile} {
				terminal, err := client.Start(t.Context(), startWorkerOffer(a, mode))
				if err != nil || terminal.Disposition != api.StartDispositionTerminal || terminal.GrantID != "" || !terminal.Deadline.IsZero() || terminal.ReceiptID != receipt.ReceiptID {
					t.Fatal("terminal notification received another executable grant", err)
				}
			}
			monitor, _ := f.store.Get(record.Intent.MonitorID)
			want := persistence.Succeeded
			if status == "unknown" {
				want = persistence.Unknown
			}
			if monitor.Actions[record.Intent.ActionID].State != want {
				t.Fatal("result did not update original action", monitor.Actions[record.Intent.ActionID].State)
			}
		})
	}
}

func TestWorkerBlueReplacementPreservesDeadlineAndFencesOldOffer(t *testing.T) {
	for _, offered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unoffered", true: "offered"}[offered], func(t *testing.T) {
			f := newWorkerSessionFixtureWithSource(t, "notification", "Monitor")
			record := queueWorkerHTTPActionSource(t, f, "notification", true, true)
			_, client, _ := startWorkerProductionServer(t, f, true, nil)
			poll := f.request
			pollNext := func() *api.Assignments {
				t.Helper()
				batch, err := client.Poll(t.Context(), poll)
				if err != nil {
					t.Fatal("poll", err)
				}
				poll.SessionID = batch.SessionID
				poll.PollSequence++
				return batch
			}
			first := pollNext()
			if len(first.Items) != 1 || first.Items[0].Notification == nil || first.Items[0].Notification.Review.Phase != "open" {
				t.Fatal("missing blue opening context")
			}
			opening := first.Items[0]
			start, err := client.Start(t.Context(), startWorkerOffer(opening, api.StartModeBegin))
			if err != nil || start.Disposition != api.StartDispositionGranted {
				t.Fatal("opening start", err)
			}
			_, err = client.Result(t.Context(), api.Outcome{ServerID: opening.ServerID, WorkerUID: opening.WorkerUID, ExecutionID: opening.ExecutionID, GrantID: start.GrantID, Kind: "notification", Status: "delivered", Evidence: []string{}, Data: json.RawMessage(`"private-schema-canary"`)})
			if err != nil {
				t.Fatal("opening result", err)
			}
			pulse := func(outcome string) persistence.Monitor {
				t.Helper()
				m, _ := f.store.Get(record.Intent.MonitorID)
				results, err := f.store.Submit(t.Context(), []persistence.Command{{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, Guard: &record.Intent.Guard, At: time.Now().UTC(), Outcome: outcome}})
				if err != nil || results[0].Err != nil {
					t.Fatal("pulse", err, results)
				}
				return *results[0].Monitor
			}
			prepare := func(m persistence.Monitor, action persistence.Action) persistence.WorkerExecutionRecord {
				t.Helper()
				intent := record.Intent.Clone()
				intent.ID, intent.Revision, intent.ActionID = uuid.NewString(), uuid.NewString(), action.ID
				intent.Scheduled, intent.Deadline = action.NotBefore, action.Deadline
				intent.Notification = nil
				intent.Payload = persistence.WorkerExecutionIntent{}.Payload
				prepared, err := f.handler.catalog.PrepareWorkerExecution(t.Context(), intent)
				if err != nil {
					t.Fatal("prepare closure", err)
				}
				saved, err := f.store.CommitWorkerExecution(t.Context(), prepared)
				if err != nil {
					t.Fatal("commit closure", err)
				}
				return saved
			}
			m := pulse("success")
			var closure persistence.Action
			for _, a := range m.Actions {
				if a.ReviewNotice != nil && a.ReviewNotice.Phase == "close" && a.State == persistence.Queued {
					closure = a
				}
			}
			if closure.ID == "" {
				t.Fatal("no verified-health closure")
			}
			old := prepare(m, closure)
			var oldOffer api.Assignment
			if offered {
				batch := pollNext()
				if len(batch.Items) != 1 {
					t.Fatal("closure offer missing")
				}
				oldOffer = batch.Items[0]
			}
			m = pulse("failure")
			if _, err := f.store.FinalizeWorkerExecutions(t.Context(), time.Now().UTC(), 128); err != nil {
				t.Fatal("retire stale closure", err)
			}
			retired, _, err := f.store.WorkerExecution(t.Context(), old.Intent.ID)
			if err != nil || retired.Finalization == nil || retired.Finalization.Reason != "notification_changed" {
				t.Fatal("stale record not finalized", err)
			}
			if offered {
				terminal, err := client.Start(t.Context(), startWorkerOffer(oldOffer, api.StartModeBegin))
				if err != nil || terminal.Disposition != api.StartDispositionTerminal || terminal.GrantID != "" {
					t.Fatal("old message received execution grant", err)
				}
			}
			m, _ = f.store.Get(m.ID)
			alert := m.Notifications.ReviewAlerts[closure.ReviewNotice.AlertID]
			replacement := m.Actions[alert.ClosingDelivery]
			if replacement.ID == closure.ID || !replacement.Deadline.Equal(closure.Deadline) {
				t.Fatal("replacement changed original budget")
			}
			prepare(m, replacement)
			batch := pollNext()
			if len(batch.Items) != 1 || batch.Items[0].Notification.Review.Health != "unhealthy" {
				t.Fatal("replacement offer lost current health")
			}
			grant, err := client.Start(t.Context(), startWorkerOffer(batch.Items[0], api.StartModeBegin))
			if err != nil || grant.Disposition != api.StartDispositionGranted || !grant.Deadline.Equal(closure.Deadline) {
				t.Fatal("replacement did not retain authorized deadline", err)
			}
		})
	}
}

func TestWorkerNotificationCiphertextBindsContext(t *testing.T) {
	for name, change := range map[string]func(*persistence.WorkerNotification){
		"health":      func(n *persistence.WorkerNotification) { n.Review.Health = "healthy" },
		"correlation": func(n *persistence.WorkerNotification) { n.CorrelationID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkerSessionFixtureWithSource(t, "notification", "Monitor")
			record := queueWorkerHTTPActionSource(t, f, "notification", true, true)
			if _, err := f.handler.catalog.OpenWorkerExecution(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			changed := record.Clone()
			change(changed.Intent.Notification)
			if _, err := f.handler.catalog.OpenWorkerExecution(t.Context(), changed); err == nil {
				t.Fatal("changed context authenticated against original ciphertext")
			}
			// A failed integrity check deliberately closes admission. Use a separate
			// store for each corruption schedule, not the live protocol fixture.
		})
	}
}
