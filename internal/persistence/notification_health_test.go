package persistence

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func closureHealthFixture(t *testing.T, color string, version int) (Monitor, Action, string, time.Time) {
	t.Helper()
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Intervention = color == "blue"
	config.Policy.Endpoints = map[string]int{"red": 1, "green": 1}
	if color == "blue" {
		config.Policy.Endpoints = map[string]int{"blue": 1}
	}
	at := policyTime()
	r := transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at}, version)
	if r.Err != nil || r.Monitor == nil || !r.Allowed {
		t.Fatal("configure closure fixture", r.Err)
	}
	m := *r.Monitor
	apply := func(c Command) {
		c.MonitorID, c.Revision = m.ID, m.Revision
		if c.Kind == "pulse" {
			c.Generation = m.Generation + 1
		}
		if c.Kind == "start" {
			c.NoticeRevision = ActionNoticeRevision(m.Actions[c.ActionID])
		}
		r := transitionAtFormat(m, c, version)
		if r.Err != nil || r.Monitor == nil || c.Kind == "start" && !r.Allowed {
			t.Fatal("closure fixture", c.Kind, r.Err)
		}
		m = *r.Monitor
	}
	apply(Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "failure"})
	recoveryID := ""
	if color == "blue" {
		recovery := interventionAction(t, m)
		recoveryID = recovery.ID
		apply(Command{Kind: "start", ActionID: recovery.ID, At: at.Add(2 * time.Second)})
		apply(Command{Kind: "result", ActionID: recovery.ID, At: at.Add(3 * time.Second), Outcome: "failure", Ambiguous: true})
	}
	openingColor := "red"
	if color == "blue" {
		openingColor = "blue"
	}
	opening := notificationByColor(t, m, openingColor, Queued)
	apply(Command{Kind: "start", ActionID: opening.ID, At: at.Add(4 * time.Second)})
	apply(Command{Kind: "result", ActionID: opening.ID, At: at.Add(5 * time.Second), Outcome: "success"})
	apply(Command{Kind: "pulse", At: at.Add(6 * time.Second), Outcome: "success"})
	apply(Command{Kind: "pulse", At: at.Add(7 * time.Second), Outcome: "success"})
	return m, notificationByColor(t, m, color, Queued).Clone(), recoveryID, at.Add(7 * time.Second)
}

func TestQueuedClosureHealthTracksPreparationAvailability(t *testing.T) {
	for _, color := range []string{"green", "blue"} {
		t.Run(color, func(t *testing.T) {
			m, before, recoveryID, at := closureHealthFixture(t, color, NotificationConditionFormatVersion)
			lastCheck, lastOutcome, closed := m.LastCheck, m.LastOutcome, m.IncidentClosedAt
			sequence, count := m.Sequence, len(m.Actions)
			m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(time.Second), Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_missing")})
			if m.observedHealth() != "healthy" || m.notificationHealth() != "unknown" || m.LastCheck != lastCheck || m.LastOutcome != lastOutcome || m.IncidentClosedAt != closed || m.Sequence != sequence || len(m.Actions) != count {
				t.Fatal("preparation changed a historical observation or admitted work")
			}
			want := before.Clone()
			if want.Resolution != nil {
				want.Resolution.Health = "unknown"
			} else {
				want.ReviewNotice.Health = "unknown"
				alert := m.Notifications.ReviewAlerts[want.ReviewNotice.AlertID]
				if alert.Health != "unknown" || !m.Actions[recoveryID].Held() {
					t.Fatal("blue closure lost availability or unknown recovery hold")
				}
			}
			got := m.Actions[before.ID]
			if !reflect.DeepEqual(got, want) || ActionNoticeRevision(got) == ActionNoticeRevision(before) {
				t.Fatal("refresh changed identity, deadline, recovery fact or more than notice health")
			}
			start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: before.ID, At: at.Add(2 * time.Second), NoticeRevision: ActionNoticeRevision(before)}
			stale := transitionAtFormat(m, start, NotificationConditionFormatVersion)
			if stale.Allowed || stale.Err != nil {
				t.Fatal("old queue copy started after current health changed", stale.Err)
			}
			start.NoticeRevision = ActionNoticeRevision(got)
			fresh := transitionAtFormat(m, start, NotificationConditionFormatVersion)
			if !fresh.Allowed || fresh.Err != nil || fresh.Monitor == nil || !fresh.Monitor.Actions[before.ID].Deadline.Equal(start.At.Add(before.ExecutionBudget)) {
				t.Fatal("honest closure lost its original start budget", fresh.Err)
			}
		})
	}
}

func TestStartedClosurePayloadIsNotRewrittenByPreparation(t *testing.T) {
	for _, color := range []string{"green", "blue"} {
		t.Run(color, func(t *testing.T) {
			m, closure, recoveryID, at := closureHealthFixture(t, color, NotificationConditionFormatVersion)
			m = conditionTransition(t, m, Command{Kind: "start", ActionID: closure.ID, At: at.Add(time.Second)})
			before := m.Actions[closure.ID].Clone()
			m = conditionTransition(t, m, Command{Kind: "pulse", At: at.Add(2 * time.Second), Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_unreadable")})
			if !reflect.DeepEqual(before, m.Actions[closure.ID]) || recoveryID != "" && !m.Actions[recoveryID].Held() {
				t.Fatal("preparation rewrote an executing notice or released a hold")
			}
		})
	}
}

func TestBlueClosureRetryRefreshesHealthWithoutRenewingBudget(t *testing.T) {
	for _, outcome := range []string{"failure", "configuration_problem"} {
		t.Run(outcome, func(t *testing.T) {
			m, closure, recoveryID, at := closureHealthFixture(t, "blue", NotificationConditionFormatVersion)
			m = conditionTransition(t, m, Command{Kind: "start", ActionID: closure.ID, At: at.Add(time.Second)})
			deadline := m.Actions[closure.ID].Deadline
			m = conditionTransition(t, m, Command{Kind: "result", ActionID: closure.ID, At: at.Add(2 * time.Second), Outcome: "failure", Retryable: true})
			retry := notificationByColor(t, m, "blue", Queued).Clone()
			if retry.Attempt != 2 || retry.Deadline != deadline || !m.Notifications.ReviewAlerts[closure.ReviewNotice.AlertID].ClosingStarted {
				t.Fatal("fixture did not retain the first start and bounded retry")
			}
			c := Command{Kind: "pulse", At: at.Add(3 * time.Second), Outcome: outcome}
			wantHealth := "unhealthy"
			if outcome == "configuration_problem" {
				c.Failure, wantHealth = NewFailureDiagnostic("ca_file_missing"), "unknown"
			}
			m = conditionTransition(t, m, c)
			updated := m.Actions[retry.ID]
			want := retry.Clone()
			want.ReviewNotice.Health = wantHealth
			if !reflect.DeepEqual(updated, want) || !m.Actions[recoveryID].Held() {
				t.Fatal("retry lost identity, original deadline, current health or action hold")
			}
			startAt := retry.NotBefore
			m = conditionTransition(t, m, Command{Kind: "start", ActionID: retry.ID, At: startAt})
			if got := m.Actions[retry.ID]; got.State != Started || got.Deadline != deadline || got.DeliveryID != closure.DeliveryID {
				t.Fatal("eligible closure retry was lost or renewed")
			}
		})
	}
}

func TestNoticeAvailabilityPreservesOlderReplayAndContext(t *testing.T) {
	for _, version := range []int{ActionFormatVersion, NotificationConditionFormatVersion} {
		m, closure, _, at := closureHealthFixture(t, "blue", version)
		before := reviewContext(m, at)
		key := reviewStateKey(m, at)
		c := Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at.Add(time.Second), Outcome: "configuration_problem", Failure: NewFailureDiagnostic("ca_file_missing")}
		r := transitionAtFormat(m, c, version)
		if r.Err != nil || r.Monitor == nil {
			t.Fatal(r.Err)
		}
		m = *r.Monitor
		captured := reviewContext(m, c.At)
		if err := validateReviewContext(captured, c.At); err != nil || captured.observedHealth() != "healthy" {
			t.Fatal("captured context lost historical health or canonical validity", err)
		}
		if version == ActionFormatVersion {
			oldBytes, _ := json.Marshal(before)
			newBytes, _ := json.Marshal(captured)
			if string(oldBytes) != string(newBytes) || reviewStateKey(m, c.At) != key || !reflect.DeepEqual(closure, m.Actions[closure.ID]) {
				t.Fatal("older context bytes, state key or notice replay changed")
			}
		} else {
			if captured.notificationHealth() != "unknown" || captured.ConfigurationProblem != c.Failure || captured.ConfigurationProblemAt != c.At || reviewStateKey(m, c.At) == key {
				t.Fatal("current context did not capture unavailable notice health")
			}
			bad := captured.Clone()
			bad.ConfigurationProblem.Message = "unreviewed diagnostic"
			if validateReviewContext(bad, c.At) == nil {
				t.Fatal("captured availability accepted an arbitrary error message")
			}
		}
	}
}

func TestReviewContextNotificationVersionValidation(t *testing.T) {
	m, _, _, at := closureHealthFixture(t, "blue", NotificationConditionFormatVersion)
	context := reviewContext(m, at)
	key := reviewPrefix("fact", m.ID) + "00000000000000000001-0000000000"
	for _, test := range []struct {
		name    string
		version int
		valid   bool
	}{
		{name: "historical", version: 0, valid: true},
		{name: "current", version: NotificationConditionFormatVersion, valid: true},
		{name: "negative", version: -1},
		{name: "unknown", version: 99},
		{name: "action storage", version: ActionFormatVersion},
		{name: "tagged storage", version: 47},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured := context.Clone()
			captured.Notifications.Version = test.version
			row := reviewRecord{Key: key, MonitorID: m.ID, Sequence: 1, Fact: &reviewFact{
				ID: key, PolicyID: identity("policy"), At: at, Fence: 1,
				Context: captured, Phase: "actions", Revision: 1,
			}}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			if got := validateReviewContext(captured, at) == nil; got != test.valid {
				t.Fatal("unexpected compact context version acceptance", test.version, got)
			}
			if _, err := encodeReviewRecord(row); (err == nil) != test.valid {
				t.Fatal("unexpected record encode version acceptance", test.version, err)
			}
			if _, err := decodeReviewRecord(data); (err == nil) != test.valid {
				t.Fatal("unexpected record decode version acceptance", test.version, err)
			}
		})
	}
}
