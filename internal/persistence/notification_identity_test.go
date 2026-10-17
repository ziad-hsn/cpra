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

func TestNotificationProviderCorrelationSurvivesRecoveryAndRenewal(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy, p.Healthy = false, 1, 1
	p.Endpoints = map[string]int{"red": 1, "green": 1}
	p.NotificationEndpointIDs = map[string][]string{"red": {"destination"}, "green": {"destination"}}
	at := policyTime().Add(time.Second)
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	red := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, red, at)
	m = policyPulse(t, m, at.Add(time.Second), "success")
	green := notificationByColor(t, m, "green", Queued)
	if red.NotificationCorrelation == "" || red.NotificationCorrelation != green.NotificationCorrelation || red.EndpointID != green.EndpointID {
		t.Fatal("confirmed recovery cannot close the original provider incident")
	}
	m = policyPulse(t, m, at.Add(2*time.Second), "failure")
	next := notificationByColor(t, m, "red", Queued)
	if next.NotificationCorrelation == red.NotificationCorrelation {
		t.Fatal("new outage inherited old provider correlation")
	}
}

func TestNotificationProviderCorrelationSurvivesWarningEscalation(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy, p.Healthy = false, 1, 1
	p.Endpoints = map[string]int{"yellow": 1, "red": 1, "green": 1}
	p.NotificationEndpointIDs = map[string][]string{"yellow": {"destination"}, "red": {"destination"}, "green": {"destination"}}
	at := policyTime().Add(time.Second)
	m := configuredPolicyMonitor(t, p)
	r := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at, Outcome: "success", Warning: true})
	m = *r.Monitor
	yellow := notificationByColor(t, m, "yellow", Queued)
	m = policyStart(t, m, yellow, at)
	m = policyOutcome(t, m, yellow, at.Add(time.Millisecond), "success", false)
	m = policyPulse(t, m, at.Add(time.Second), "failure")
	red := notificationByColor(t, m, "red", Queued)
	if red.NotificationEpisode == yellow.NotificationEpisode || red.NotificationCorrelation != yellow.NotificationCorrelation || red.EndpointID != yellow.EndpointID {
		t.Fatalf("severity escalation orphaned the delivered provider warning or reused its notification episode: yellow=%+v red=%+v", yellow, red)
	}
	m = policyPulse(t, m, at.Add(2*time.Second), "success")
	green := notificationByColor(t, m, "green", Queued)
	if green.NotificationCorrelation != yellow.NotificationCorrelation || green.EndpointID != yellow.EndpointID {
		t.Fatal("confirmed recovery cannot close the warning that began this failure episode")
	}
	m = policyPulse(t, m, at.Add(3*time.Second), "failure")
	next := notificationByColor(t, m, "red", Queued)
	if next.NotificationCorrelation == yellow.NotificationCorrelation {
		t.Fatal("a subsequent outage inherited the recovered warning's provider identity")
	}
}

func TestNotificationConfigureRejectsOldEnvelopeFields(t *testing.T) {
	mutations := map[string]func(*Monitor){
		"version":     func(m *Monitor) { m.NotificationVersion = DeliveryFormatVersion; m.NotificationEpisode = "episode" },
		"episode":     func(m *Monitor) { m.NotificationEpisode = "episode" },
		"correlation": func(m *Monitor) { m.NotificationCorrelation = "correlation" },
		"maintenance": func(m *Monitor) { m.NotificationMaintenance = true },
		"endpoints": func(m *Monitor) {
			m.Policy.Endpoints = map[string]int{"red": 1}
			m.Policy.NotificationEndpointIDs = map[string][]string{"red": {"destination"}}
		},
		"action_delivery":    func(m *Monitor) { m.Actions = map[string]Action{"action": {DeliveryID: "delivery"}} },
		"action_episode":     func(m *Monitor) { m.Actions = map[string]Action{"action": {NotificationEpisode: "episode"}} },
		"action_correlation": func(m *Monitor) { m.Actions = map[string]Action{"action": {NotificationCorrelation: "correlation"}} },
		"action_endpoint":    func(m *Monitor) { m.Actions = map[string]Action{"action": {EndpointID: "endpoint"}} },
		"action_deadline":    func(m *Monitor) { m.Actions = map[string]Action{"action": {Deadline: policyTime().Add(time.Minute)}} },
		"action_queued_at":   func(m *Monitor) { m.Actions = map[string]Action{"action": {QueuedAt: policyTime()}} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := testMonitor()
			mutate(&m)
			c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
			if !deliveryStorageFormat(commandMinimumFormat(c)) {
				t.Fatal("delivery field missed minimum-format selection")
			}
			raw, err := json.Marshal(envelope{Version: NotificationFormatVersion, Commands: []Command{c}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeEnvelope(raw); err == nil {
				t.Fatal("old envelope accepted delivery-format configuration fields")
			}
		})
	}
	m := testMonitor()
	m.NotificationVersion = DeliveryFormatVersion + 100
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
	if err := validateCommand(c); err == nil {
		t.Fatal("malformed notification version reached committed application")
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

func TestNotificationRetryAfterMaintenanceRemainsSuppressed(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy, p.Healthy = false, 1, 1
	p.Endpoints = map[string]int{"red": 1, "green": 1}
	at := policyTime().Add(time.Second)
	p.Maintenance = []MaintenanceWindow{{Start: at.Add(2 * time.Second), End: at.Add(3 * time.Second)}}
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	a := notificationByColor(t, m, "red", Queued)
	m = policyStart(t, m, a, at)
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "failure", Retryable: true})
	m = *r.Monitor
	retry := notificationByColor(t, m, "red", Queued)
	if retry.NotBefore.Before(p.Maintenance[0].End) || !retry.QueuedAt.Equal(at) {
		t.Fatal("fixture did not retain original delivery admission across the window")
	}
	r = transition(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, At: retry.NotBefore})
	if r.Allowed || r.Monitor.Actions[retry.ID].State != Cancelled || r.Monitor.Actions[retry.ID].Outcome != "maintenance" {
		t.Fatal("retry released a notification accumulated before maintenance")
	}
	m = policyPulse(t, *r.Monitor, at.Add(6*time.Second), "success")
	m = policyPulse(t, m, at.Add(7*time.Second), "failure")
	next := notificationByColor(t, m, "red", Queued)
	_ = policyStart(t, m, next, next.NotBefore)
}

func TestNotificationMaintenanceSuppressesNewInterventionResultAlerts(t *testing.T) {
	p := testMonitor().Policy
	p.Intervention, p.Unhealthy = true, 1
	p.Endpoints = map[string]int{"cyan": 1, "red": 1}
	at := policyTime().Add(time.Second)
	m := policyPulse(t, configuredPolicyMonitor(t, p), at, "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, at)
	r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "success", Maintenance: true})
	if r.Monitor.VerifyRemaining == 0 {
		t.Fatal("maintenance discarded the known intervention outcome")
	}
	for _, a := range r.Monitor.Actions {
		if a.Kind == "code" && a.State == Queued {
			t.Fatal("result committed a new notification inside maintenance")
		}
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
	m.Policy.NotificationEndpointIDs = map[string][]string{"red": {"destination"}}
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: policyTime().Add(time.Second)})
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(lifecycleSnapshotMagic)) {
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
