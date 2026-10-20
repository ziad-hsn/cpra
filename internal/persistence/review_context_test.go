package persistence

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

func TestReviewContextBoundsEscapedDestinationsAndDropsHistory(t *testing.T) {
	m := configuredPolicyMonitor(t, lifecyclePolicy())
	m.Policy.Endpoints = map[string]int{"blue": 10000, "red": 10000}
	ids := make([]string, 10000)
	for n := range ids {
		ids[n] = strings.Repeat("\x01", 256)
	}
	m.Policy.NotificationEndpointIDs = map[string][]string{"blue": ids, "red": ids}
	m.Notifications.Deliveries = map[string]DeliveryState{"unrelated": {Episode: strings.Repeat("x", 2<<20)}}
	m.ActionProjection = &ActionProjection{LatestRecovery: &Action{ID: strings.Repeat("y", 2<<20)}}
	at := policyTime()
	ledger := newLedgerTest(t, false, 32<<20)
	policy := testReviewPolicy(t, ledger, &m)
	key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
	fact := &reviewFact{ID: key, PolicyID: m.ReviewPolicyID, At: at, Fence: 1, Context: reviewContext(m, at), Phase: "actions", Revision: 1}
	row := reviewRecord{Key: key, MonitorID: m.ID, Sequence: 1, Fact: fact}
	data, err := encodeReviewRecord(row)
	if err != nil || len(data) > 1<<20 {
		t.Fatal("bounded context failed", len(data), err)
	}
	if bytes.Contains(data, []byte("unrelated")) || bytes.Contains(data, []byte("action_projection")) || bytes.Contains(data, []byte("notification_endpoint_ids")) {
		t.Fatal("unrelated policy/history entered assessment")
	}
	decoded, err := decodeReviewRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, append(policy, row)); err != nil {
		t.Fatal(err)
	}
	f := machine{collections: ledger}
	for _, n := range []int{0, 9999} {
		restored, err := f.reviewInput(*decoded.Fact, n)
		if err != nil || notificationEndpointIdentity(restored, "blue", n) != notificationEndpointIdentity(m, "blue", n) || restored.reviewTimeouts[n] != m.Policy.notificationTimeout("blue", n) {
			t.Fatal("destination identity or budget changed")
		}
	}
}

func TestReviewContextCapturesHealthAndSuppressionWithoutWallClockReplay(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(-time.Second), End: at.Add(time.Second)}}
	c := reviewContext(m, at)
	if c.observedHealth() != m.observedHealth() || c.outageQualified() != m.outageQualified() || !c.notificationsSuppressed(at.Add(time.Hour)) {
		t.Fatal("committed assessment changed with wall clock")
	}
}

func TestMonitorCloneDetachesOriginalOwnership(t *testing.T) {
	original := ""
	m := Monitor{AdoptedFileIncarnation: &original, Notifications: &NotificationState{ReviewAlerts: map[string]ReviewAlert{"review": {Owner: &reviewAlertOwner{}}}}}
	copy := m.Clone()
	*copy.AdoptedFileIncarnation = "replacement"
	copy.Notifications.ReviewAlerts["review"].Owner.CatalogUID = "replacement"
	if *m.AdoptedFileIncarnation != "" || m.Notifications.ReviewAlerts["review"].Owner.CatalogUID != "" {
		t.Fatal("ownership pointer escaped immutable copy")
	}
	data, _ := json.Marshal(m)
	var roundtrip Monitor
	if err := json.Unmarshal(data, &roundtrip); err != nil || roundtrip.AdoptedFileIncarnation == nil || *roundtrip.AdoptedFileIncarnation != "" {
		t.Fatal("original-file alias lost presence", err)
	}
}

func TestReviewContextBoundsAdmittedFailureAndHistoricalCounts(t *testing.T) {
	for _, count := range []int{10001, math.MaxInt32} {
		m := configuredPolicyMonitor(t, lifecyclePolicy())
		m.Policy.Endpoints = map[string]int{"blue": count}
		m.LastOutcome = strings.Repeat("untrusted failure", 150000)
		ledger := newLedgerTest(t, false, 1<<20)
		rows := testReviewPolicy(t, ledger, &m)
		if len(rows) != 1 || rows[0].Policy.Offset != -1 || rows[0].Policy.Explicit {
			t.Fatal("ordinal-only historical policy allocated destination rows")
		}
		if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, rows); err != nil {
			t.Fatal(err)
		}
		at := policyTime()
		c := reviewContext(m, at)
		data, err := json.Marshal(c)
		if err != nil || len(data) > 4096 || c.LastOutcome != "failure" || validateReviewContext(c, at) != nil {
			t.Fatal("unbounded admitted outcome entered assessment", len(data), err)
		}
		f := machine{collections: ledger}
		input, err := f.reviewInput(reviewFact{Context: c, PolicyID: m.ReviewPolicyID, Fence: 1}, count-1)
		if err != nil || notificationEndpointIdentity(input, "blue", count-1) != notificationEndpointIdentity(m, "blue", count-1) {
			t.Fatal("historical endpoint count changed identity", err)
		}
	}
}

func TestReviewContextRejectsMalformedAndUnrelatedState(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	c := reviewContext(m, at)
	for name, change := range map[string]func(*Monitor){
		"inline revision":        func(m *Monitor) { m.Revision = "inline" },
		"oversized revision":     func(m *Monitor) { m.Revision = strings.Repeat("r", 257) },
		"invalid owner":          func(m *Monitor) { m.CatalogUID = "bad\nowner" },
		"invalid installation":   func(m *Monitor) { m.InstallationID = "bad\ninstallation" },
		"missing incident":       func(m *Monitor) { m.Incident = true; m.IncidentID = "" },
		"closed active incident": func(m *Monitor) { m.Incident = true; m.IncidentClosedAt = at },
		"history":                func(m *Monitor) { m.Actions = map[string]Action{"old": {ID: "old"}} },
		"delivery":               func(m *Monitor) { m.Notifications.Deliveries = map[string]DeliveryState{"old": {}} },
		"foreign qualification":  func(m *Monitor) { m.Notifications.QualifiedIncident = "another-incident" },
	} {
		t.Run(name, func(t *testing.T) {
			broken := c.Clone()
			change(&broken)
			if validateReviewContext(broken, at) == nil {
				t.Fatal("malformed compact context accepted")
			}
		})
	}
}

func TestReviewPolicyCapturesSameRevisionChangesWithoutHotPathInvalidation(t *testing.T) {
	m, _, at := unknownRecoveryWithBlue(t)
	m.Policy.NotificationEndpointIDs = map[string][]string{"blue": {"original"}}
	m.Policy.NotificationTimeouts = map[string][]time.Duration{"blue": {time.Second}}
	ledger := newLedgerTest(t, false, 1<<20)
	rows := testReviewPolicy(t, ledger, &m)
	if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 1}, nil, rows); err != nil {
		t.Fatal(err)
	}
	fact := reviewFact{Context: reviewContext(m, at), PolicyID: m.ReviewPolicyID, Fence: 1}
	// A routine check compares the cached policy identity, never invalidates it.
	checked := transition(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at.Add(time.Millisecond), Outcome: "failure"})
	if checked.Monitor == nil || checked.Monitor.ReviewPolicyID != m.ReviewPolicyID {
		t.Fatal("routine check invalidated immutable policy")
	}
	configuration := m.Clone()
	configuration.Policy.NotificationEndpointIDs["blue"][0] = "replacement"
	configuration.Policy.NotificationTimeouts["blue"][0] = time.Hour
	configured := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: at.Add(time.Second), Config: &configuration})
	if configured.Monitor == nil || configured.Monitor.ReviewPolicyID != "" {
		t.Fatal("same-revision configuration retained stale policy identity")
	}
	next := configured.Monitor
	rows = testReviewPolicy(t, ledger, next)
	if _, err := ledger.CommitActionState(actionLedgerPosition{Index: 2}, nil, rows); err != nil {
		t.Fatal(err)
	}
	if reviewStateKey(m, at) == reviewStateKey(*next, at) {
		t.Fatal("new destinations/budgets did not request reassessment")
	}
	f := machine{collections: ledger}
	original, err := f.reviewInput(fact, 0)
	if err != nil || notificationEndpointIdentity(original, "blue", 0) != notificationEndpointIdentity(m, "blue", 0) || original.reviewTimeouts[0] != time.Second {
		t.Fatal("delayed fact borrowed current destination or extended deadline", err)
	}
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.validateReviewPolicy(fact.Context, fact.PolicyID, 1); err != nil {
		t.Fatal(err)
	}
	if err := view.validateReviewPolicy(*next, next.ReviewPolicyID, 2); err != nil {
		t.Fatal(err)
	}
	if err := view.validateReviewPolicy(*next, next.ReviewPolicyID, 1); err == nil {
		t.Fatal("new policy spliced into older assessment fence")
	}
}

func TestReviewPolicySnapshotRejectsStaleCurrentBinding(t *testing.T) {
	s := openCatalogMemory(t)
	m := testMonitor()
	m.Policy.Endpoints = map[string]int{"blue": 1}
	m.Policy.NotificationEndpointIDs = map[string][]string{"blue": {"original"}}
	m.Policy.NotificationTimeouts = map[string][]time.Duration{"blue": {time.Second}}
	mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()})
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	f := frozen.(*frozenSnapshot)
	for _, field := range []string{"endpoint", "budget", "mode"} {
		t.Run(field, func(t *testing.T) {
			original := f.image.Monitors[m.ID]
			changed := original.Clone()
			switch field {
			case "endpoint":
				changed.Policy.NotificationEndpointIDs["blue"][0] = "other"
			case "budget":
				changed.Policy.NotificationTimeouts["blue"][0] = time.Hour
			case "mode":
				changed.Policy.NotificationEndpointIDs, changed.Policy.NotificationTimeouts = nil, nil
			}
			f.image.Monitors[m.ID] = changed
			if err := f.Persist(&collectionTestSink{}); err == nil {
				t.Fatal("stale current policy binding accepted")
			}
			f.image.Monitors[m.ID] = original
			if err := f.Persist(&collectionTestSink{}); err != nil {
				t.Fatal("historical policy facts no longer readable", err)
			}
		})
	}
}

func TestReviewPolicyPreservesHistoricalRevisionThroughMigration(t *testing.T) {
	for _, previousAction := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "unknown"}[previousAction], func(t *testing.T) {
			cfg := testConfig(t)
			s, err := openReplayStore(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			m := testMonitor()
			m.Revision = strings.Repeat("old-revision-", 30)
			m.Policy.Endpoints = map[string]int{"blue": 1}
			m.Policy.Intervention = true
			at := time.Now().UTC()
			command := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}
			if _, err := encodedBound(envelope{Version: LifecycleFormatVersion, Commands: []Command{command}}); err != nil {
				t.Fatal("fixture was not historically admissible", err)
			}
			if result := applyCatalogFormat(t, s, LifecycleFormatVersion, command)[0]; result.Err != nil {
				t.Fatal(result.Err)
			}
			var action Action
			if previousAction {
				result := applyCatalogFormat(t, s, LifecycleFormatVersion, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at, Outcome: "failure"})[0]
				action = interventionAction(t, *result.Monitor)
				applyCatalogFormat(t, s, LifecycleFormatVersion, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at})
				applyCatalogFormat(t, s, LifecycleFormatVersion, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at, Outcome: "failure", Ambiguous: true})
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), cfg)
			if err != nil {
				t.Fatal("historical store could not migrate", err)
			}
			defer s.Close()
			current, _ := s.Get(m.ID)
			if current.Revision != m.Revision || !s.Status().Ready {
				t.Fatal("migration changed the original configuration binding")
			}
			// New observations still use the original exact execution revision.
			if result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: current.Generation + 1, At: at.Add(time.Second), Outcome: "failure"})[0]; result.Err != nil || result.Monitor == nil {
				t.Fatal("historical observation failed", result.Err)
			}
			current = reconcileReviewFacts(t, s, m.ID, at.Add(2*time.Second))
			if previousAction {
				if retained := retainedAction(t, s, action.ID); retained.Revision != m.Revision || !retained.Held() {
					t.Fatal("migration altered uncertain evidence")
				}
				found := false
				for _, a := range current.Actions {
					if a.ReviewNotice != nil {
						found = true
						if a.Revision != m.Revision || a.EndpointID != notificationEndpointIdentity(current, "blue", 0) {
							t.Fatal("new notice borrowed or truncated configuration identity")
						}
					}
				}
				if !found {
					t.Fatal("historical unknown action lost its eligible review notification")
				}
			}
			raw := projectionSnapshot(t, s)
			if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
				t.Fatal("migrated revision was not snapshot-readable", err)
			}
		})
	}
}

func TestReviewPolicyLargeHistoricalRevisionLifecycle(t *testing.T) {
	cfg := testConfig(t)
	s, err := openReplayStore(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	m := testMonitor()
	// Writer formats before the configure ID cap admitted revisions near half
	// the 4 MiB command envelope. This input must remain readable and usable.
	m.Revision = strings.Repeat("r", 2095000)
	m.Policy.Endpoints, m.Policy.Intervention = map[string]int{"blue": 1}, true
	at := time.Now().UTC()
	configuration := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at}
	raw, err := json.Marshal(envelope{Version: LifecycleFormatVersion, Commands: []Command{configuration}})
	if err != nil || len(raw) > maxCommitBytes {
		t.Fatal("historical command exceeds original envelope", len(raw), err)
	}
	if result := applyCatalogFormat(t, s, LifecycleFormatVersion, configuration)[0]; result.Err != nil {
		t.Fatal(result.Err)
	}
	result := applyCatalogFormat(t, s, LifecycleFormatVersion, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: at, Outcome: "failure"})[0]
	if result.Err != nil || result.Monitor == nil {
		t.Fatal(result.Err)
	}
	recovery := interventionAction(t, *result.Monitor)
	for _, command := range []Command{
		{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at},
		{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: recovery.ID, At: at.Add(time.Millisecond), Outcome: "failure", Ambiguous: true},
	} {
		if r := applyCatalogFormat(t, s, LifecycleFormatVersion, command)[0]; r.Err != nil || command.Kind == "start" && !r.Allowed {
			t.Fatal(command.Kind, r.Err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal("large historical revision could not migrate", err)
	}
	current, _ := s.Get(m.ID)
	if result := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: current.Generation + 1, At: at.Add(time.Second), Outcome: "failure"})[0]; result.Err != nil {
		t.Fatal("historical observation failed", result.Err)
	}
	current = reconcileReviewFacts(t, s, m.ID, at.Add(time.Second))
	opening := notificationByColor(t, current, "blue", Queued)
	if opening.Revision != m.Revision || opening.EndpointID != notificationEndpointIdentity(current, "blue", 0) {
		t.Fatal("original blue execution binding changed")
	}
	for _, command := range []Command{
		{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, NoticeRevision: ActionNoticeRevision(opening), ExecutorSession: s.executorSession, At: at.Add(time.Second)},
		{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: opening.ID, At: at.Add(2 * time.Second), Outcome: "success"},
	} {
		if r := submit(t, s, command)[0]; r.Err != nil || command.Kind == "start" && !r.Allowed {
			t.Fatal(command.Kind, r.Err)
		}
	}
	if delivered := retainedAction(t, s, opening.ID); delivered.Revision != m.Revision || delivered.State != Succeeded {
		t.Fatal("large delivery result lost")
	}
	if !retainedAction(t, s, recovery.ID).Held() {
		t.Fatal("delivery released unknown recovery")
	}
	image := projectionSnapshot(t, s)
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(image))); err != nil {
		t.Fatal("large original binding lost during snapshot restore", err)
	}
	if delivered := retainedAction(t, s, opening.ID); delivered.Revision != m.Revision || delivered.State != Succeeded {
		t.Fatal("large delivery changed on restoration")
	}
}
