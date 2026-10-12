package persistence

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func notificationByColor(t *testing.T, m Monitor, color string, state ActionState) Action {
	t.Helper()
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == color && a.State == state {
			return a
		}
	}
	t.Fatalf("missing %s %s notification", state, color)
	return Action{}
}

func TestNotificationEpisodeCancelsObsoleteWorkAndBypassesOldCooldown(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy, p.Healthy = false, 1, 1
	p.Cooldown = 5 * time.Minute
	p.Endpoints = map[string]int{"red": 1, "green": 1}
	at := policyTime().Add(time.Second)
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	first := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, first, at)
	m = policyOutcome(t, m, first, at.Add(time.Millisecond), "success", false)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	green := notificationByColor(t, m, "green", Queued)
	m = policyPulse(t, m, at.Add(2*time.Second), "failure")
	second := notificationByColor(t, m, "red", Queued)
	if second.IncidentID == first.IncidentID || second.NotificationEpisode == first.NotificationEpisode || second.DeliveryID == first.DeliveryID || second.EndpointID != first.EndpointID || !second.NotBefore.Equal(at.Add(2*time.Second)) {
		t.Fatal("new outage reused identity or waited for the previous outage's cooldown")
	}
	if m.Actions[green.ID].State != Cancelled {
		t.Fatal("previous recovery can still be delivered during a new outage")
	}
	m = policyPulse(t, m, at.Add(3*time.Second), "success")
	if m.Actions[second.ID].State != Cancelled {
		t.Fatal("red remains queued after confirmed recovery")
	}
	r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: second.ID, At: at.Add(5 * time.Minute)})
	if r.Allowed || m.Actions[second.ID].State != Cancelled {
		t.Fatal("obsolete red notification could start after recovery")
	}
}

func TestNotificationMaintenanceCancelsUnsentWorkAndKeepsObservations(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy = false, 1
	p.Endpoints = map[string]int{"red": 1, "yellow": 1}
	start := policyTime().Add(time.Minute)
	p.Maintenance = []MaintenanceWindow{{Start: start, End: start.Add(time.Minute)}}
	m := policyPulse(t, configuredPolicyMonitor(t, p), start.Add(-time.Second), "failure")
	red := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, red, red.NotBefore)
	yellow := notificationByColor(t, m, "yellow", Queued)
	m = policyPulse(t, m, start, "failure")
	if m.TotalChecks != 2 || m.Actions[yellow.ID].State != Cancelled || m.Actions[red.ID].State != Started {
		t.Fatal("maintenance lost health observations or modified an already started action")
	}
	for _, a := range m.Actions {
		if a.Kind == "code" && a.State == Queued {
			t.Fatal("maintenance queued a deferred notification")
		}
	}
	m = policyPulse(t, m, start.Add(time.Minute), "failure")
	if m.Actions[yellow.ID].State != Cancelled {
		t.Fatal("maintenance end revived unsent work")
	}
}

func TestNotificationStartCommitsMaintenanceCancellation(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy = false, 1
	p.Endpoints = map[string]int{"red": 1}
	at := policyTime().Add(time.Second)
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	a := notificationByColor(t, m, "red", Queued)
	r := transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Maintenance: true})
	if r.Allowed || r.Monitor.Actions[a.ID].State != Cancelled || r.Monitor.Actions[a.ID].Outcome != "maintenance" {
		t.Fatal("start merely deferred an alert covered by maintenance")
	}
}

func TestNotificationRetriesKeepLogicalIdentityAndOverallDeadline(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy = false, 1
	p.Endpoints = map[string]int{"red": 1}
	at := policyTime().Add(time.Second)
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	first := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, first, at)
	first = m.Actions[first.ID]
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: first.ID, At: at.Add(time.Second), Outcome: "failure", Retryable: true})
	m = *r.Monitor
	retry := notificationByColor(t, m, "red", Queued)
	if retry.ID == first.ID || retry.DeliveryID != first.DeliveryID || retry.NotificationEpisode != first.NotificationEpisode || retry.EndpointID != first.EndpointID || retry.Deadline != first.Deadline || retry.Attempt != 2 {
		t.Fatal("retry changed logical identity or extended its overall budget")
	}
	r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, At: retry.NotBefore, ActionDeadline: retry.Deadline.Add(time.Second)})
	if r.Err != ErrNotificationIdentity || r.Allowed {
		t.Fatal("retry could extend its deadline")
	}
	r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, At: retry.Deadline})
	if r.Allowed || r.Monitor.Actions[retry.ID].State != Cancelled || r.Monitor.Actions[retry.ID].Outcome != "notification_deadline" {
		t.Fatal("expired notification can still start")
	}
}

func TestNotificationFormatMigrationCancelsOnlyUnsentWork(t *testing.T) {
	config := testMonitor()
	config.Policy.Unhealthy, config.Policy.Intervention = 1, false
	config.Policy.Endpoints = map[string]int{"red": 1, "yellow": 1}
	at := policyTime().Add(time.Second)
	r := transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: policyTime()}, NotificationFormatVersion)
	r = transitionAtFormat(*r.Monitor, Command{Kind: "pulse", MonitorID: config.ID, Revision: config.Revision, At: at, Generation: 1, Outcome: "failure"}, NotificationFormatVersion)
	m := *r.Monitor
	red := notificationByColor(t, m, "red", Queued)
	yellow := notificationByColor(t, m, "yellow", Queued)
	r = transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: red.ID, At: at}, NotificationFormatVersion)
	prior := r.Monitor.Actions[red.ID]
	r = transition(*r.Monitor, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, At: at.Add(time.Second)})
	if r.Monitor.NotificationVersion != DeliveryFormatVersion || r.Monitor.Actions[yellow.ID].State != Cancelled || !reflect.DeepEqual(r.Monitor.Actions[red.ID], prior) {
		t.Fatal("migration replayed or erased a started action instead of cancelling only unsent work")
	}
	for _, e := range r.Events {
		if e.Type != "action_cancelled" {
			t.Fatal("migration created external intent")
		}
	}
}

func TestNotificationIdentitySnapshotRejectsOldFormat(t *testing.T) {
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy.Unhealthy, m.Policy.Intervention = 1, false
	m.Policy.Endpoints = map[string]int{"red": 1}
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: policyTime().Add(time.Second)})
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(deliverySnapshotMagic)) {
		t.Fatal("snapshot did not record its new storage boundary")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	i.Version = NotificationFormatVersion
	old, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImage(bytes.NewReader(old)); err == nil {
		t.Fatal("older format accepted episode/deadline state")
	}
}
