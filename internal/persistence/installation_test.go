package persistence

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestInstallationSeparatesIndependentProviderIdentities(t *testing.T) {
	first, second := openCatalogMemory(t), openCatalogMemory(t)
	at := policyTime()
	create := func(s *Store) Monitor {
		m := testMonitor()
		commands := []Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at},
			{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at.Add(time.Second), Outcome: "failure"}}
		for _, result := range submit(t, s, commands...) {
			if result.Err != nil || result.Monitor == nil {
				t.Fatal("fixture admission failed", result.Err)
			}
		}
		m, _ = s.Get(m.ID)
		return m
	}
	a, b := create(first), create(second)
	if a.InstallationID == "" || a.InstallationID == b.InstallationID || a.NotificationCorrelation == b.NotificationCorrelation {
		t.Fatal("independent installations share provider identity")
	}
	for id := range a.Actions {
		if _, exists := b.Actions[id]; exists {
			t.Fatal("independent installations share action identity")
		}
	}
	for _, original := range []Monitor{a, b} {
		m := original.Clone()
		m.Policy.Endpoints["gray"] = 1
		m.queueEnabledNotice(at.Add(2*time.Second), func(string, *Action) {})
		if m.EnabledControl == original.EnabledControl || m.EnabledControl == "" {
			t.Fatal("missing namespaced control notice")
		}
	}
}

func TestInstallationUpgradePreservesExposureAndActionIdentity(t *testing.T) {
	for _, started := range []bool{false, true} {
		s := openCatalogMemory(t)
		at := policyTime()
		m := testMonitor()
		m.Policy.Endpoints = map[string]int{"red": 1, "green": 1}
		applyCatalogFormat(t, s, LifecycleFormatVersion,
			Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at},
			Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at.Add(time.Second), Outcome: "failure"})
		before, _ := s.Get(m.ID)
		id := sortedActions(before.Actions)[0]
		if started {
			applyCatalogFormat(t, s, LifecycleFormatVersion, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: id, At: at.Add(time.Second)})
			before, _ = s.Get(m.ID)
		}
		if err := s.ensureInstallation(t.Context()); err != nil {
			t.Fatal(err)
		}
		after, _ := s.Get(m.ID)
		if after.InstallationID == "" || after.NotificationCorrelation != before.NotificationCorrelation || after.NotificationAttempted != before.NotificationAttempted || !reflect.DeepEqual(after.Actions, before.Actions) {
			t.Fatal("namespace migration rewrote existing effect evidence")
		}
		// Namespace assignment is only one startup phase. Current notifications
		// require the same committed exposure conversion performed by Open.
		if err := s.migrateActionProjection(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := s.migrateNotificationConditions(t.Context()); err != nil {
			t.Fatal(err)
		}
		for n := 2; n <= 3; n++ {
			submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: uint64(n), At: at.Add(time.Duration(n) * time.Second), Outcome: "success"})
		}
		after, _ = s.Get(m.ID)
		if (queuedColorCount(after, "green") == 1) != started {
			t.Fatal("migration manufactured or discarded historical notification exposure")
		}
		if started {
			green := notificationByColor(t, after, "green", Queued)
			if green.NotificationCorrelation != before.Actions[id].NotificationCorrelation || green.ProviderCorrelationVersion != before.Actions[id].ProviderCorrelationVersion {
				t.Fatal("recovery changed an exposed provider incident key")
			}
		}
	}
}

func TestInstallationSurvivesSnapshotAndCompleteStoppedRestore(t *testing.T) {
	config := testConfig(t)
	s, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, s)
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: at, Outcome: "failure"})
	before, _ := s.Get("stable-one")
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restoredConfig := testConfig(t)
	copyRestoreFixture(t, config.Storage.Directory, restoredConfig.Storage.Directory)
	if err := MarkRestored(restoredConfig.Storage.Directory, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenAdministrative(t.Context(), restoredConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, _ := restored.Get(before.ID)
	if after.InstallationID != before.InstallationID || restored.installationID != before.InstallationID || after.NotificationCorrelation != before.NotificationCorrelation {
		t.Fatal("complete restore changed installation or provider correlation")
	}
	view, err := restored.fsm.collections.pinActions()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	for id, old := range before.Actions {
		row, exists, err := view.Get(t.Context(), id)
		if err != nil || !exists || row.Action.NotificationCorrelation != old.NotificationCorrelation || row.Action.DeliveryID != old.DeliveryID {
			t.Fatal("restore changed original delivery identity", err)
		}
	}
}

func TestInstallationUpgradeDoesNotReopenExistingBlue(t *testing.T) {
	for _, started := range []bool{false, true} {
		m := configuredPolicyMonitor(t, lifecyclePolicy())
		m.Policy.Intervention = true
		m.Policy.Endpoints["blue"] = 1
		at := policyTime().Add(time.Second)
		m = policyPulse(t, m, at, "failure")
		var recovery Action
		for _, a := range m.Actions {
			if a.Kind == "intervention" {
				recovery = a
			}
		}
		m = policyStart(t, m, recovery, at)
		r := transition(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at.Add(time.Second), Outcome: "failure", Ambiguous: true})
		if r.Monitor == nil {
			t.Fatal("missing unknown recovery")
		}
		m = *r.Monitor
		opening := notificationByColor(t, m, "blue", Queued)
		if started {
			m = policyStart(t, m, opening, at.Add(time.Second))
			m = policyOutcome(t, m, opening, at.Add(2*time.Second), "success", false)
		}
		// Namespace admission leaves old alert/delivery identities unchanged.
		m.InstallationID = "new-installation-namespace"
		m = policyPulse(t, m, at.Add(3*time.Second), "failure")
		if len(m.Notifications.ReviewAlerts) != 1 {
			t.Fatal("namespace admission duplicated the review condition")
		}
		if !started && notificationByColor(t, m, "blue", Queued).ID != opening.ID || started && queuedColorCount(m, "blue") != 0 {
			t.Fatal("namespace admission duplicated the opening")
		}
		m = policyPulse(t, m, at.Add(4*time.Second), "success")
		m = policyPulse(t, m, at.Add(5*time.Second), "success")
		if started {
			closure := notificationByColor(t, m, "blue", Queued)
			if closure.ReviewNotice.Phase != "close" || closure.NotificationCorrelation != opening.NotificationCorrelation || closure.EndpointID != opening.EndpointID {
				t.Fatal("closure lost the original provider identity")
			}
		} else if queuedColorCount(m, "blue") != 0 {
			t.Fatal("unexposed opening acquired a closure")
		}
	}
}

func TestInstallationCommandAndSnapshotFormatGates(t *testing.T) {
	c := Command{Kind: "installation", InstallationID: "installation-fixture", At: policyTime()}
	for _, format := range []int{42, ActionFormatVersion} {
		encoded, err := json.Marshal(envelope{Version: format, Commands: []Command{c}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(encoded)
		if (err == nil) != (format == ActionFormatVersion) {
			t.Fatal("wrong namespace command format gate", format, err)
		}
	}
	c.MonitorID = "unrelated"
	if validateCommand(c) == nil {
		t.Fatal("installation command admitted unrelated fields")
	}
	c.Kind = "recover"
	if validateCommand(c) == nil {
		t.Fatal("namespace leaked into an unrelated command")
	}
	s := openCatalogMemory(t)
	configure(t, s)
	i := s.fsm.image
	i.InstallationID = "mismatched"
	encoded, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImageValue(json.NewDecoder(bytes.NewReader(encoded))); err == nil {
		t.Fatal("mismatched monitor namespace passed snapshot validation")
	}
}
