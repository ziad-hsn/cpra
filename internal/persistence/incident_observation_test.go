package persistence

import (
	"testing"
	"time"
)

func TestIncidentObservationSeparatesFailureThresholdAndDelivery(t *testing.T) {
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy.Unhealthy, m.Policy.Intervention = 3, false
	m.Policy.Endpoints = map[string]int{"red": 1}
	at := time.Now().UTC()
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}).Monitor
	for generation := uint64(1); generation <= 3; generation++ {
		result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: generation, Outcome: "failure", At: at.Add(time.Duration(generation) * time.Second)})[0]
		if result.Err != nil || result.Monitor == nil {
			t.Fatal("health observation rejected", result.Err)
		}
		m = *result.Monitor
		observed, found, err := s.IncidentContext(t.Context(), m.IncidentID)
		if err != nil || !found || !observed.FirstFailureAt.Equal(at.Add(time.Second)) {
			t.Fatal("first failure missing", err)
		}
		if generation < 3 && (observed.Active || !observed.OpenedAt.IsZero()) {
			t.Fatal("first failure was presented as an opened incident")
		}
		if generation == 3 && (!observed.Active || !observed.OpenedAt.Equal(at.Add(3*time.Second))) {
			t.Fatal("threshold time missing", observed)
		}
	}
	frozen, err := s.IncidentSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var notice Action
	for _, a := range m.Actions {
		if a.Color == "red" {
			notice = a
		}
	}
	if notice.ID == "" {
		t.Fatal("no red delivery")
	}
	mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: notice.ID, At: at.Add(4 * time.Second)})
	completed := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: notice.ID, Outcome: "success", At: at.Add(5 * time.Second)})[0]
	if completed.Err != nil || completed.Monitor == nil || completed.Monitor.Actions[notice.ID].State != Succeeded {
		t.Fatal("delivery result rejected", completed.Err)
	}
	before, _ := frozen.Get(m.IncidentID)
	current, _, err := s.IncidentContext(t.Context(), m.IncidentID)
	if err != nil || !before.NotificationsAvailable || before.Notifications.Pending != 1 || before.Notifications.Accepted != 0 || current.Notifications.Pending != 0 || current.Notifications.Accepted != 1 {
		t.Fatal("incident page did not freeze actual notification states", before, current, err)
	}
}

func TestOlderIncidentDoesNotInventThresholdTime(t *testing.T) {
	for _, kind := range []string{"success", "failure", "configure", "recover", "result"} {
		t.Run(kind, func(t *testing.T) {
			m := testMonitor()
			m.Policy.Unhealthy, m.Policy.Intervention = 1, kind == "result"
			at := time.Now().UTC()
			m = *transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}, LifecycleFormatVersion).Monitor
			m = *transitionAtFormat(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at}, LifecycleFormatVersion).Monitor
			command := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: kind, At: at.Add(time.Second)}
			switch kind {
			case "configure":
				command = Command{Kind: kind, MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at.Add(time.Second)}
			case "recover":
				command = Command{Kind: kind, MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second)}
			case "result":
				a := interventionAction(t, m)
				m = *transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at}, LifecycleFormatVersion).Monitor
				command = Command{Kind: kind, MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "failure", At: at.Add(time.Second)}
			}
			result := transition(m, command)
			if result.Err != nil || result.Monitor == nil {
				t.Fatal("upgrade failed", result.Err)
			}
			m = *result.Monitor
			if !m.IncidentConfirmedAt.IsZero() || m.IncidentThresholdTracked || !incidentOf(m).OpenedAt.IsZero() {
				t.Fatal("upgrade inferred an unrecorded threshold time")
			}
			if !m.IncidentActive() || incidentOf(m).NotificationsAvailable {
				t.Fatal("upgrade lost existing incident activity or invented complete counts")
			}
		})
	}
}

func TestManualRecoveryDoesNotInventThresholdTime(t *testing.T) {
	for _, outcome := range []string{"failure", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s := openCatalogMemory(t)
			m, guard := manualFixture(t, s)
			at := policyTime().Add(2 * time.Second)
			m = *mustResult(t, s, manualCommand(m, guard, "manual-before-threshold", at)).Monitor
			a := latestManual(t, m)
			mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: guard, At: at})
			r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "failure", Ambiguous: outcome == "unknown", At: at.Add(time.Second)})[0]
			if r.Err != nil || r.Monitor == nil || !r.Monitor.IncidentActive() || !r.Monitor.IncidentConfirmedAt.IsZero() {
				t.Fatal("manual escalation invented threshold confirmation or lost activity", r.Err)
			}
			want := Failed
			if outcome == "unknown" {
				want = Unknown
			}
			if r.Monitor.Actions[a.ID].State != want {
				t.Fatal("fixture did not commit the expected outcome", r.Monitor.Actions[a.ID].State)
			}
			incident, found, err := s.IncidentContext(t.Context(), m.IncidentID)
			if err != nil || !found || !incident.Active || !incident.OpenedAt.IsZero() || incident.FirstFailureAt.IsZero() {
				t.Fatal("manual escalation was presented as reaching the threshold", incident, err)
			}
		})
	}
}

func TestIncidentNotificationCountsRetainCancelledDelivery(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	at := time.Now().UTC()
	for generation := uint64(1); generation <= 3; generation++ {
		result := submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: generation,
			Outcome: "failure", Maintenance: generation == 2, At: at.Add(time.Duration(generation) * time.Second)})[0]
		if result.Err != nil || result.Monitor == nil {
			t.Fatal(result.Err)
		}
		counts := result.Monitor.IncidentNotifications()
		if generation == 2 && (counts.Cancelled != 2 || counts.Pending != 0) {
			t.Fatal("suppression lost cancelled deliveries", counts)
		}
		if generation == 3 && (counts.Cancelled != 2 || counts.Pending != 2) {
			t.Fatal("readmission lost prior logical deliveries", counts)
		}
	}
}

func TestIncidentNotificationRetryCountsSurviveSnapshot(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	configure(t, s)
	at := time.Now().UTC()
	m := *submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, Outcome: "failure", At: at})[0].Monitor
	var original Action
	for _, a := range m.Actions {
		if a.Color == "red" && a.Endpoint == 0 {
			original = a
		}
	}
	mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: original.ID, At: at})
	m = *submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: original.ID, Outcome: "failure", Retryable: true, At: at.Add(time.Second)})[0].Monitor
	if counts := m.IncidentNotifications(); counts.Pending != 2 || counts.Failed != 0 {
		t.Fatal("retry counted as a separate logical delivery", counts)
	}
	var retry Action
	for _, a := range m.Actions {
		if a.DeliveryID == original.DeliveryID {
			retry = a
		}
	}
	if retry.Attempt != 2 {
		t.Fatal("fixture did not create a retry")
	}
	mustResult(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, At: retry.NotBefore})
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: retry.ID, Outcome: "success", At: retry.NotBefore.Add(time.Second)})
	if err = s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	incident, found, err := s.IncidentContext(t.Context(), m.IncidentID)
	if err != nil || !found || incident.Notifications.Accepted != 1 || incident.Notifications.Pending != 1 || incident.Notifications.Failed != 0 {
		t.Fatal("snapshot changed logical delivery counts", incident, err)
	}
	retained, found, err := s.ActionContext(t.Context(), original.ID)
	if err != nil || !found || retained.State != Failed {
		t.Fatal("earlier attempt was discarded", err)
	}
}
