package persistence

import (
	"testing"
	"time"
)

// The fields below are the pre-delivery-identity record written by b293f34.
// No modern episode, endpoint, deadline or provider-key fields are fabricated.
func preDeliveryMonitor(state ActionState) (Monitor, Action) {
	at := policyTime()
	m := testMonitor()
	m.Name = "svcA"
	m.Policy = Policy{Enabled: true, Interval: time.Second, Unhealthy: 1, Healthy: 2, Endpoints: map[string]int{"red": 1, "green": 1}}
	m.IncidentID, m.Incident, m.Recovering = "old-incident", true, true
	m.IncidentOpenedAt = at
	m.IncidentRevision = m.Revision
	m.IncidentSequence = 1
	m.LastOutcome, m.LastCheck = "failure", at
	a := Action{ID: "old-delivery", Revision: m.Revision, IncidentID: m.IncidentID, Kind: "code", Color: "red", Endpoint: 0,
		Attempt: 1, State: state, NotBefore: at}
	if state != Queued {
		a.StartedAt = at.Add(time.Second)
	}
	if state == Succeeded || state == Unknown {
		a.FinishedAt = at.Add(2 * time.Second)
	}
	m.Actions = map[string]Action{a.ID: a}
	return m, a
}

func configureMigratedMonitor(t *testing.T, m Monitor, changed bool, driver string) Monitor {
	t.Helper()
	config := m.Clone()
	if changed {
		config.Revision += "-changed"
	}
	config.Policy.NotificationEndpointIDs = map[string][]string{"red": {"original-destination"}, "green": {"original-destination"}}
	config.Policy.NotificationDrivers = map[string][]string{"red": {driver}, "green": {driver}}
	r := transitionAtFormat(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: policyTime().Add(10 * time.Second)}, NotificationConditionFormatVersion)
	if r.Err != nil || !r.Allowed || r.Monitor == nil {
		t.Fatal("migration configure", r.Err)
	}
	return *r.Monitor
}

func TestDeliveryMigrationPreservesOriginalPagerDutyKey(t *testing.T) {
	for _, state := range []ActionState{Succeeded, Unknown} {
		t.Run(string(state), func(t *testing.T) {
			m, original := preDeliveryMonitor(state)
			digest := ActionReviewRevision(original)
			m = configureMigratedMonitor(t, m, false, "pagerduty")
			if ActionReviewRevision(m.Actions[original.ID]) != digest || m.Notifications.Migrations[original.ID].ReviewRevision != digest {
				t.Fatal("migration rewrote historical provider evidence")
			}
			m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(11 * time.Second), Outcome: "failure"})
			if queuedColorCount(m, "red") != 0 {
				t.Fatal("upgrade issued a replacement trigger")
			}
			for _, n := range []int{12, 13} {
				m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Duration(n) * time.Second), Outcome: "success"})
			}
			closing := notificationByColor(t, m, "green", Queued)
			if closing.ProviderKey != "cpra:svcA" || closing.IncidentID != original.IncidentID {
				t.Fatal("closure lost the original PagerDuty identity", closing)
			}
			if err := validateNotificationIdentity(m, NotificationConditionFormatVersion); err != nil {
				t.Fatal("invalid migrated monitor", err)
			}
		})
	}
}

func TestDeliveryMigrationHoldsUnprovenBinding(t *testing.T) {
	for _, changed := range []bool{false, true} {
		m, _ := preDeliveryMonitor(Unknown)
		driver := "opsgenie"
		if changed {
			driver = "pagerduty"
		}
		m = configureMigratedMonitor(t, m, changed, driver)
		for _, n := range []int{11, 12} {
			m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(time.Duration(n) * time.Second), Outcome: "success"})
		}
		if queuedColorCount(m, "green") != 0 || len(m.Notifications.Resolutions) != 1 {
			t.Fatal("unproven migration produced a provider operation", changed)
		}
		for _, v := range m.Notifications.Resolutions {
			if v.MigrationReason == "" {
				t.Fatal("migration uncertainty was hidden")
			}
		}
	}
}

func TestDeliveryMigrationUnstartedActionDoesNotEstablishExposure(t *testing.T) {
	m, old := preDeliveryMonitor(Queued)
	m = configureMigratedMonitor(t, m, false, "pagerduty")
	if m.Actions[old.ID].State != Cancelled || m.greenEligible() {
		t.Fatal("migration manufactured notification exposure")
	}
	m = conditionTransition(t, m, Command{Kind: "pulse", At: policyTime().Add(11 * time.Second), Outcome: "failure"})
	if queuedColorCount(m, "red") != 1 {
		t.Fatal("fresh evidence could not establish a new condition for proven unsent work")
	}
}

func TestDeliveryMigrationDoesNotBlockAnotherDestination(t *testing.T) {
	m, _ := preDeliveryMonitor(Unknown)
	m.Policy.Endpoints["red"] = 2
	m = configureMigratedMonitor(t, m, false, "pagerduty")
	// Existing evidence proves only destination zero. The second independent
	// destination remains eligible without reusing the first delivery identity.
	if !m.migratedOpeningHeld("red", 0) || m.migratedOpeningHeld("red", 1) {
		t.Fatal("migration hold escaped its proven destination")
	}
}

func TestDeliveryMigrationSelectsCompatibleOpening(t *testing.T) {
	m, red := preDeliveryMonitor(Succeeded)
	yellow := red
	yellow.ID, yellow.Color = "a-first-yellow", "yellow"
	m.Actions[yellow.ID] = yellow
	m.Policy.Endpoints["yellow"] = 1
	m.Policy.NotificationEndpointIDs = map[string][]string{"red": {"pd-red"}, "green": {"pd-green"}, "yellow": {"chat"}}
	m.Policy.NotificationDrivers = map[string][]string{"red": {"pagerduty"}, "green": {"pagerduty"}, "yellow": {"log"}}
	m = conditionTransition(t, m, Command{Kind: "configure", Config: &m, At: policyTime().Add(10 * time.Second)})
	for _, resolution := range m.Notifications.Resolutions {
		if resolution.ProviderKey != "cpra:svcA" || resolution.MigrationReason != "" || resolution.OpeningActionID != red.ID {
			t.Fatalf("unrelated opening blocked proven original key: %+v", resolution)
		}
	}
}

func TestDeliveryMigrationMultipleHistoricalClosuresHoldDeterministically(t *testing.T) {
	for range 20 {
		m, original := preDeliveryMonitor(Succeeded)
		green := original
		green.ID, green.Color = "a-green", "green"
		m.Actions[green.ID] = green
		green.ID, green.State = "z-green", Unknown
		m.Actions[green.ID] = green
		m = configureMigratedMonitor(t, m, false, "pagerduty")
		for _, resolution := range m.Notifications.Resolutions {
			if resolution.ActionID != "a-green" || resolution.State != Unknown || resolution.MigrationReason != "migration_delivery_evidence_conflict" {
				t.Fatalf("historical closure evidence was nondeterministic: %+v", resolution)
			}
		}
	}
}
