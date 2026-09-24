package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func lateResultFixture(t *testing.T) (Monitor, Action, Command) {
	t.Helper()
	p := testMonitor().Policy
	p.Intervention = true
	m := configuredPolicyMonitor(t, p)
	m = policyPulse(t, m, policyTime().Add(time.Second), "failure")
	a := interventionAction(t, m)
	m = policyStart(t, m, a, a.NotBefore)
	a = m.Actions[a.ID]
	config := m.Clone()
	config.Revision = "new-target-revision"
	result := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: a.StartedAt.Add(time.Second)})
	if result.Monitor == nil || result.Monitor.Actions[a.ID].State != Unknown {
		t.Fatal("fixture did not preserve the started action as unknown")
	}
	command := Command{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID,
		ExecutionStart: a.StartedAt.Add(time.Millisecond), ExecutionEnd: a.StartedAt.Add(2 * time.Second), At: a.StartedAt.Add(3 * time.Second), Outcome: "success"}
	return *result.Monitor, a, command
}

func TestLateResultPreservesOriginalActionAndCurrentIncident(t *testing.T) {
	for _, scenario := range []string{"changed target", "removed", "recreated", "confirmed rejection"} {
		t.Run(scenario, func(t *testing.T) {
			m, action, command := lateResultFixture(t)
			switch scenario {
			case "removed":
				m.Removed, m.Policy.Enabled = true, false
			case "recreated":
				old := m.Actions[action.ID]
				old.CatalogUID = "original-incarnation"
				m.Actions[action.ID] = old
				m.CatalogUID = "new-incarnation"
				m.Incident, m.Generation, m.Sequence = true, 7, 55
			case "confirmed rejection":
				command.Outcome = "failure"
			}
			before := m.Clone()
			result := transition(m, command)
			if !result.Allowed || result.Err != nil || result.Monitor == nil || len(result.Events) != 1 {
				t.Fatal("known late evidence was not recorded", result.Err)
			}
			after := result.Monitor.Clone()
			recorded := after.Actions[action.ID]
			if recorded.State != Unknown || recorded.LateEvidence == nil || recorded.LateEvidence.ExecutionEnd != command.ExecutionEnd {
				t.Fatal("late evidence resolved the held action or lost completion time")
			}
			wantOutcome := "accepted"
			if command.Outcome == "failure" {
				wantOutcome = "rejected"
			}
			if recorded.LateEvidence.Outcome != wantOutcome || result.Events[0].Outcome != wantOutcome ||
				result.Events[0].Revision != action.Revision || result.Events[0].CatalogUID != before.Actions[action.ID].CatalogUID {
				t.Fatal("late history does not identify the original action/outcome")
			}
			recorded.LateEvidence = nil
			after.Actions[action.ID] = recorded
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before, m) {
				t.Fatal("late evidence changed current configuration/incident or caller state")
			}
			// Exported copies and subsequently frozen snapshots must not share a
			// mutable evidence pointer with the authoritative action.
			copy := result.Monitor.Clone()
			copy.Actions[action.ID].LateEvidence.Outcome = "tampered"
			if result.Monitor.Actions[action.ID].LateEvidence.Outcome != wantOutcome {
				t.Fatal("late evidence escaped through a shallow clone")
			}
		})
	}
}

func TestLateResultIdempotenceAndConflictingEvidence(t *testing.T) {
	m, action, command := lateResultFixture(t)
	first := transition(m, command)
	for _, duplicate := range []Command{command, func() Command { c := command; c.At = c.At.Add(time.Minute); return c }()} {
		result := transition(*first.Monitor, duplicate)
		if !result.Allowed || result.Err != nil || result.Monitor != nil || len(result.Events) != 0 {
			t.Fatal("identical known outcome was appended again", result.Err)
		}
	}
	command.Outcome = "failure"
	result := transition(*first.Monitor, command)
	if !errors.Is(result.Err, ErrLateEvidenceConflict) || result.Allowed || result.Monitor != nil || len(result.Events) != 0 ||
		first.Monitor.Actions[action.ID].LateEvidence.Outcome != "accepted" {
		t.Fatal("contradictory evidence silently replaced the first outcome", result.Err)
	}
}

func TestLateResultRejectsUnstartedWrongIdentityAndInvalidTimes(t *testing.T) {
	m, action, command := lateResultFixture(t)
	for _, mutate := range []func(*Monitor, *Command){
		func(m *Monitor, c *Command) {
			a := m.Actions[action.ID]
			a.StartedAt = time.Time{}
			m.Actions[action.ID] = a
		},
		func(m *Monitor, c *Command) { a := m.Actions[action.ID]; a.State = Cancelled; m.Actions[action.ID] = a },
		func(m *Monitor, c *Command) { a := m.Actions[action.ID]; a.State = Succeeded; m.Actions[action.ID] = a },
		func(m *Monitor, c *Command) { c.Revision = m.Revision },
		func(m *Monitor, c *Command) { c.ActionID = "different-action" },
		func(m *Monitor, c *Command) { c.MonitorID = "different-monitor" },
		func(m *Monitor, c *Command) { c.ExecutionStart = action.StartedAt.Add(-time.Nanosecond) },
	} {
		current, c := m.Clone(), command
		mutate(&current, &c)
		if result := transition(current, c); result.Allowed || result.Monitor != nil || len(result.Events) != 0 {
			t.Fatal("invalid original execution accepted as late evidence")
		}
	}
	for _, mutate := range []func(*Command){
		func(c *Command) { c.ExecutionStart = time.Time{} },
		func(c *Command) { c.ExecutionEnd = c.ExecutionStart.Add(-time.Nanosecond) },
		func(c *Command) { c.At = c.ExecutionEnd.Add(-time.Nanosecond) },
		func(c *Command) { c.Outcome = "timeout" },
		func(c *Command) { c.Ambiguous = true },
		func(c *Command) { c.Retryable = true },
		func(c *Command) { c.Driver = "provider diagnostic/secret" },
	} {
		c := command
		mutate(&c)
		if err := validateCommand(c); err == nil {
			t.Fatal("malformed/ambiguous late command would enter durable storage")
		}
	}
}

func TestLateResultSnapshotValidation(t *testing.T) {
	m, action, command := lateResultFixture(t)
	result := transition(m, command)
	for _, mutate := range []func(*Action){
		func(a *Action) { a.LateEvidence.Outcome = "provider response text" },
		func(a *Action) { a.LateEvidence.ExecutionStart = a.StartedAt.Add(-time.Second) },
		func(a *Action) { a.LateEvidence.ExecutionEnd = a.LateEvidence.ExecutionStart.Add(-time.Second) },
		func(a *Action) { a.LateEvidence.RecordedAt = a.LateEvidence.ExecutionEnd.Add(-time.Second) },
		func(a *Action) { a.State = Queued },
	} {
		copy := result.Monitor.Clone()
		a := copy.Actions[action.ID]
		mutate(&a)
		copy.Actions[action.ID] = a
		raw, _ := json.Marshal(image{Version: FormatVersion, Monitors: map[string]Monitor{copy.ID: copy}})
		if _, err := decodeImage(strings.NewReader(string(raw))); err == nil {
			t.Fatal("corrupt or executable late evidence accepted in snapshot")
		}
	}
}

func TestLateResultPersistsThroughSnapshotRestartWithoutRepeatingHistory(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m := testMonitor()
	m.Policy.Intervention = true
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: now})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, At: now.Add(time.Second), Outcome: "failure"})
	m, _ = s.Get(m.ID)
	a := interventionAction(t, m)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: now.Add(time.Second)})
	config := m.Clone()
	config.Revision = "new-target"
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: config.Revision, Config: &config, At: now.Add(2 * time.Second)})
	command := Command{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, Outcome: "success",
		ExecutionStart: now.Add(time.Second + time.Millisecond), ExecutionEnd: now.Add(3 * time.Second), At: now.Add(4 * time.Second)}
	if result := submit(t, s, command)[0]; result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(m.ID)
	if current.Revision != config.Revision || current.Actions[a.ID].State != Unknown || current.Actions[a.ID].LateEvidence == nil || current.Actions[a.ID].LateEvidence.Outcome != "accepted" {
		t.Fatal("restart lost held outcome or original late evidence")
	}
	if result := submit(t, s, command)[0]; result.Err != nil || !result.Allowed {
		t.Fatal(result.Err)
	}
	page, err := s.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range page.Events {
		if event.Type == "action_late_evidence" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("restart/repeated outcome duplicated history: %d", count)
	}
	command.Outcome = "failure"
	if result := submit(t, s, command)[0]; !errors.Is(result.Err, ErrLateEvidenceConflict) {
		t.Fatal("contradictory committed evidence accepted", result.Err)
	}
	if !s.Status().Ready {
		t.Fatal("ordinary evidence conflict poisoned storage")
	}
}
