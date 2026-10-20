package persistence

import (
	"bytes"
	"io"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func notificationTransitionFixture(t *testing.T, unfinished ActionState) (Monitor, Action, Action) {
	return notificationTransitionFixtureAtFormat(t, unfinished, DeliveryFormatVersion)
}

func notificationTransitionFixtureAtFormat(t *testing.T, unfinished ActionState, version int) (Monitor, Action, Action) {
	t.Helper()
	p := testMonitor().Policy
	p.Unhealthy, p.Intervention, p.RecoveryMaxAttempts = 1, true, 3
	p.Cooldown = time.Minute
	p.Endpoints = map[string]int{"yellow": 2}
	at := policyTime().Add(time.Second)
	config := testMonitor()
	config.Policy = p
	m := *transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: policyTime()}, version).Monitor
	apply := func(c Command) {
		c.MonitorID, c.Revision = m.ID, m.Revision
		r := transitionAtFormat(m, c, version)
		if r.Err != nil || r.Monitor == nil {
			t.Fatal("notification fixture transition failed", r.Err)
		}
		m = *r.Monitor
	}
	apply(Command{Kind: "pulse", Generation: 1, At: at, Outcome: "failure"})
	apply(Command{Kind: "start", ActionID: interventionAction(t, m).ID, At: at})
	var delivered, pending Action
	for _, action := range m.Actions {
		if action.Kind == "code" && action.Endpoint == 0 {
			delivered = action
		} else if action.Kind == "code" && action.Endpoint == 1 {
			pending = action
		}
	}
	if delivered.ID == "" || pending.ID == "" {
		t.Fatal("fixture did not queue both notification endpoints")
	}
	apply(Command{Kind: "start", ActionID: delivered.ID, At: at.Add(time.Millisecond)})
	apply(Command{Kind: "result", ActionID: delivered.ID, At: at.Add(2 * time.Millisecond), Outcome: "success"})
	if unfinished != Queued {
		apply(Command{Kind: "start", ActionID: pending.ID, At: at.Add(3 * time.Millisecond)})
	}
	if unfinished == Unknown {
		apply(Command{Kind: "result", ActionID: pending.ID, At: at.Add(4 * time.Millisecond), Outcome: "failure", Ambiguous: true})
	}
	return m, m.Actions[delivered.ID], m.Actions[pending.ID]
}

func TestDeliveredNotificationSurvivesAnotherFailedCheck(t *testing.T) {
	for _, unfinished := range []ActionState{Queued, Started, Unknown} {
		t.Run(string(unfinished), func(t *testing.T) {
			m, delivered, pending := notificationTransitionFixtureAtFormat(t, unfinished, ActionFormatVersion)
			at := policyTime().Add(time.Second)
			r := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision,
				Generation: m.Generation + 1, At: at.Add(250 * time.Millisecond), Outcome: "failure"})
			if r.Err != nil || r.Monitor == nil {
				t.Fatal("next observation was not applied", r.Err)
			}
			if action, ok := r.Monitor.Actions[delivered.ID]; !ok || action.State != Succeeded {
				t.Fatal("another failed check replaced a confirmed notification delivery")
			}
			if action, ok := r.Monitor.Actions[pending.ID]; !ok || action.State != unfinished {
				t.Fatal("another failed check changed the unfinished endpoint")
			}
			for _, event := range r.Events {
				if event.Type == "action_queued" && event.Kind == "code" {
					t.Fatal("another failed check created another notification for the same state")
				}
			}
		})
	}
}

func TestNotificationHistoricalLogReplay(t *testing.T) {
	for _, version := range []int{1, 2, 14, 31, 33, 35, NotificationFormatVersion, DeliveryFormatVersion} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			s := openReplayMemory(t)
			fixtureVersion := NotificationFormatVersion
			if version == DeliveryFormatVersion {
				fixtureVersion = DeliveryFormatVersion
			}
			m, delivered, pending := notificationTransitionFixtureAtFormat(t, Started, fixtureVersion)
			s.fsm.image.Monitors[m.ID] = m.Clone()
			r := applyCatalogFormat(t, s, version, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision,
				Generation: m.Generation + 1, At: policyTime().Add(1250 * time.Millisecond), Outcome: "failure"})[0]
			if r.Err != nil || r.Monitor == nil || !reflect.DeepEqual(r.Monitor.Actions[pending.ID], pending) {
				t.Fatal("replay changed the unfinished endpoint", r.Err)
			}
			if notificationStorageFormat(version) {
				if !reflect.DeepEqual(r.Monitor.Actions[delivered.ID], delivered) || s.fsm.image.Version != version {
					t.Fatal("new observations lost delivery or did not preserve their storage contract")
				}
				return
			}
			if _, retained := r.Monitor.Actions[delivered.ID]; retained || r.Monitor.Sequence != m.Sequence+1 {
				t.Fatal("historical replay changed its original action identities")
			}
			queued := 0
			for _, a := range r.Monitor.Actions {
				if a.Kind == "code" && a.Endpoint == delivered.Endpoint {
					queued++
					if a.State != Queued || !a.NotBefore.Equal(m.Cooldowns["yellow"].Add(m.Policy.Cooldown)) {
						t.Fatal("historical replay changed its queued action")
					}
				}
			}
			if queued != 1 {
				t.Fatal("historical replay lost its replacement action")
			}
		})
	}
}

func TestNotificationSnapshotThenNewObservation(t *testing.T) {
	s := openReplayMemory(t)
	m, delivered, pending := notificationTransitionFixture(t, Unknown)
	s.fsm.image.Monitors[m.ID] = m.Clone()
	c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1,
		At: policyTime().Add(1250 * time.Millisecond), Outcome: "failure"}
	if commandWriteFormat(c) != ActionFormatVersion || commandMinimumFormat(c) != FormatVersion {
		t.Fatal("new writer policy changed the historical reader minimum")
	}
	if r := submit(t, s, c)[0]; r.Err != nil {
		t.Fatal(r.Err)
	}
	raw := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(raw, []byte(actionSnapshotMagic)) {
		t.Fatal("snapshot did not preserve notification semantics")
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		c.Generation++
		c.At = c.At.Add(time.Second)
		r := submit(t, s, c)[0]
		if r.Err != nil || r.Monitor == nil || !reflect.DeepEqual(r.Monitor.Actions[delivered.ID], delivered) ||
			!reflect.DeepEqual(r.Monitor.Actions[pending.ID], pending) {
			t.Fatal("restored observations replaced a delivered or unknown endpoint", r.Err)
		}
		for _, e := range r.Events {
			if e.Type == "action_queued" && e.Kind == "code" {
				t.Fatal("restored unchanged state sent another notification")
			}
		}
	}
}

func TestNotificationNewIncidentCanNotifyAgain(t *testing.T) {
	p := testMonitor().Policy
	p.Unhealthy, p.Healthy, p.Intervention = 1, 1, false
	p.Endpoints = map[string]int{"yellow": 1}
	m := policyPulse(t, configuredPolicyMonitor(t, p), policyTime().Add(time.Second), "failure")
	var first Action
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == "yellow" {
			first = a
		}
	}
	if first.ID == "" {
		t.Fatal("initial incident did not notify")
	}
	m = policyStart(t, m, first, first.NotBefore)
	m = policyOutcome(t, m, first, first.NotBefore.Add(time.Millisecond), "success", false)
	m = policyPulse(t, m, policyTime().Add(2*time.Second), "success")
	if m.Recovering || m.Incident {
		t.Fatal("fixture did not recover")
	}
	m = policyPulse(t, m, policyTime().Add(2*time.Minute), "failure")
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Color == "yellow" && a.State == Queued && a.ID != first.ID && a.IncidentID != first.IncidentID {
			return
		}
	}
	t.Fatal("new incident was prevented from notifying")
}
