package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func agentActionFixture(t *testing.T, s *Store, kind string) (Monitor, Action, *LocalExecution) {
	t.Helper()
	m := testMonitor()
	m.Policy.Intervention = kind == "intervention"
	m.Policy.RecoveryMaxAttempts = 3
	m.Policy.Endpoints = map[string]int{"red": 1}
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
	for _, a := range m.Actions {
		if a.Kind != kind {
			continue
		}
		h, err := s.BeginLocalAction(context.Background(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Driver: "a2a", At: at})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Finish(context.Background()) })
		m, _ = s.Get(m.ID)
		return m, m.Actions[a.ID], h
	}
	t.Fatal("missing action")
	return Monitor{}, Action{}, nil
}

func agentReceiptCommand(a Action, sequence uint64, state string) AgentReceiptCommand {
	return AgentReceiptCommand{ExpectedSequence: sequence - 1, Receipt: AgentReceipt{Sequence: sequence,
		TaskID: "task-123", ContextID: "context-456", State: state, ObservedAt: a.StartedAt.Add(time.Duration(sequence) * time.Nanosecond)}}
}

func TestAgentReceiptCommitIsConditionalAndIdempotent(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	first := agentReceiptCommand(a, 1, "TASK_STATE_SUBMITTED")
	if err := h.RecordAgentReceipt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	page, err := s.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RecordAgentReceipt(context.Background(), first); err != nil {
		t.Fatal("same committed receipt was not idempotent", err)
	}
	duplicate, _ := s.History().Page(m.ID, "", 100)
	if len(duplicate.Events) != len(page.Events) {
		t.Fatal("duplicate receipt added history")
	}
	second := agentReceiptCommand(a, 2, "TASK_STATE_WORKING")
	for name, change := range map[string]func(*AgentReceiptCommand){
		"different task":      func(c *AgentReceiptCommand) { c.Receipt.TaskID = "another-task" },
		"different context":   func(c *AgentReceiptCommand) { c.Receipt.ContextID = "another-context" },
		"message replacement": func(c *AgentReceiptCommand) { c.Receipt.TaskID, c.Receipt.State, c.Receipt.MessageID = "", "", "reply" },
		"backwards time":      func(c *AgentReceiptCommand) { c.Receipt.ObservedAt = a.StartedAt },
		"skipped observation": func(c *AgentReceiptCommand) { c.ExpectedSequence, c.Receipt.Sequence = 3, 4 },
	} {
		t.Run(name, func(t *testing.T) {
			c := second
			change(&c)
			if err := h.RecordAgentReceipt(context.Background(), c); !errors.Is(err, ErrAgentReceiptConflict) {
				t.Fatal("conflicting observation admitted", err)
			}
		})
	}
	if err := h.RecordAgentReceipt(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordAgentReceipt(context.Background(), first); !errors.Is(err, ErrAgentReceiptConflict) {
		t.Fatal("older observation replaced newer state", err)
	}
	m, _ = s.Get(m.ID)
	if !sameAgentReceipt(*m.Actions[a.ID].AgentReceipt, second.Receipt) || m.Actions[a.ID].State != Started {
		t.Fatal("receipt changed action outcome or lost state")
	}
}

func TestAgentReceiptSnapshotAndReadsAreDetached(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	first := agentReceiptCommand(a, 1, "TASK_STATE_SUBMITTED")
	if err := h.RecordAgentReceipt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	view, err := s.ActionSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	read, _, _ := s.Action(a.ID)
	read.AgentReceipt.TaskID = "caller-edit"
	m, _ = s.Get(m.ID)
	m.Actions[a.ID].AgentReceipt.TaskID = "caller-edit"
	page, _ := s.History().Page(m.ID, "", 100)
	for _, e := range page.Events {
		if e.AgentReceipt != nil {
			e.AgentReceipt.TaskID = "caller-edit"
		}
	}
	second := agentReceiptCommand(a, 2, "TASK_STATE_WORKING")
	if err := h.RecordAgentReceipt(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	sink := &collectionTestSink{}
	if err := frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	image, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if !sameAgentReceipt(*image.Monitors[m.ID].Actions[a.ID].AgentReceipt, first.Receipt) {
		t.Fatal("snapshot was changed after freeze")
	}
	prior, _, _ := view.Get(a.ID)
	if !sameAgentReceipt(*prior.AgentReceipt, first.Receipt) {
		t.Fatal("action view was not frozen")
	}
	page, _ = s.History().Page(m.ID, "", 100)
	for _, e := range page.Events {
		if e.AgentReceipt != nil && e.AgentReceipt.TaskID != first.Receipt.TaskID {
			t.Fatal("history escaped mutable receipt")
		}
	}
}

func TestAgentReceiptFencesRemoteWorkSeparatelyFromLocalReturn(t *testing.T) {
	for _, state := range []string{"", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_FUTURE", "TASK_STATE_FAILED", "TASK_STATE_COMPLETED"} {
		t.Run(state, func(t *testing.T) {
			s := openCatalogMemory(t)
			m, a, h := agentActionFixture(t, s, "intervention")
			if state != "" {
				if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 1, state)); err != nil {
					t.Fatal(err)
				}
			}
			submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "failure", Ambiguous: true})
			if err := h.Finish(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.Action(a.ID)
			if err != nil || !ok || got.State != Unknown || !got.Held || got.ExecutorFenced != agentTerminal(state) {
				t.Fatal("local return resolved or fenced remote work", got, err)
			}
			m, _ = s.Get(m.ID)
			m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 2, Outcome: "failure", At: time.Now().UTC()})[0].Monitor
			for id, other := range m.Actions {
				if other.Kind == "intervention" && id != a.ID {
					t.Fatal("agent failure or uncertainty allowed automatic retry")
				}
			}
			if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 2, "TASK_STATE_COMPLETED")); !errors.Is(err, ErrExecutorUnfenced) {
				t.Fatal("completed local invocation accepted new observations", err)
			}
		})
	}
}

func TestAgentReceiptSuccessRequiresEvidenceForItsRole(t *testing.T) {
	for _, kind := range []string{"code", "intervention"} {
		for _, state := range []string{"missing", "message", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_FUTURE"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				s := openCatalogMemory(t)
				m, a, h := agentActionFixture(t, s, kind)
				if state != "missing" {
					c := agentReceiptCommand(a, 1, state)
					if state == "message" {
						c.Receipt.TaskID, c.Receipt.State, c.Receipt.MessageID = "", "", "agent-reply"
					}
					if err := h.RecordAgentReceipt(context.Background(), c); err != nil {
						t.Fatal(err)
					}
				}
				r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "success"})[0]
				want := Unknown
				if state == "TASK_STATE_COMPLETED" || kind == "code" && (state == "message" || state == "TASK_STATE_SUBMITTED" || state == "TASK_STATE_WORKING") {
					want = Succeeded
				}
				if r.Err != nil || r.Monitor == nil || r.Monitor.Actions[a.ID].State != want {
					t.Fatal("invented success or lost acceptance", r)
				}
			})
		}
	}
}

func TestAgentReceiptDoesNotOverrideAmbiguousResult(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	if err := h.RecordAgentReceipt(t.Context(), agentReceiptCommand(a, 1, "TASK_STATE_COMPLETED")); err != nil {
		t.Fatal(err)
	}
	r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID,
		Outcome: "success", Ambiguous: true, At: time.Now().UTC()})[0]
	if r.Monitor == nil || r.Monitor.Actions[a.ID].State != Unknown || r.Monitor.VerifyRemaining != 0 {
		t.Fatal("task receipt overrode explicit result uncertainty")
	}
}

func TestAgentReceiptCannotEnterThroughConfiguration(t *testing.T) {
	m := testMonitor()
	m.Actions = map[string]Action{"action": {ID: "action", ExecutorKind: "agent"}}
	c := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()}
	if err := validateCommand(c); err == nil {
		t.Fatal("configuration admitted an agent executor outside its versioned start")
	}
	m.Actions["action"] = Action{ID: "action", AgentReceipt: &AgentReceipt{TaskID: "ignored-plaintext"}}
	if err := validateCommand(c); err == nil {
		t.Fatal("configuration admitted receipt fields outside their command")
	}
}

func TestAgentReceiptRetainsOriginalConfiguration(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	first := agentReceiptCommand(a, 1, "TASK_STATE_SUBMITTED")
	if err := h.RecordAgentReceipt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	changed := testMonitor()
	changed.Revision = "replacement"
	submit(t, s, Command{Kind: "configure", MonitorID: changed.ID, Revision: changed.Revision, Config: &changed, At: time.Now().UTC()})
	second := agentReceiptCommand(a, 2, "TASK_STATE_COMPLETED")
	if err := h.RecordAgentReceipt(context.Background(), second); err != nil {
		t.Fatal("original action evidence lost after configuration change", err)
	}
	m, _ = s.Get(m.ID)
	old := m.Actions[a.ID]
	if m.Revision != changed.Revision || old.Revision != a.Revision || old.State != Unknown || !old.Held() || m.VerifyRemaining != 0 {
		t.Fatal("receipt changed replacement monitor or resolved old action", m)
	}
	for _, current := range m.Actions {
		if current.ID != a.ID && current.AgentReceipt != nil {
			t.Fatal("old task was attached to new action")
		}
	}
	if err := h.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Action(a.ID)
	if !got.Held || !got.ExecutorFenced {
		t.Fatal("terminal task should permit review without resolving outcome")
	}
}

func TestAgentReceiptReviewRequiresRemoteCompletion(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote-running", true: "remote-stopped"}[terminal], func(t *testing.T) {
			s := openCatalogMemory(t)
			m, guard := manualFixture(t, s)
			at := time.Now().UTC()
			m = *mustResult(t, s, manualCommand(m, guard, "agent-manual", at)).Monitor
			a := latestManual(t, m)
			h, err := s.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Guard: guard, Driver: "a2a", At: at})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Finish(context.Background()) })
			m, _ = s.Get(m.ID)
			a = m.Actions[a.ID]
			state := "TASK_STATE_WORKING"
			if terminal {
				state = "TASK_STATE_COMPLETED"
			}
			if err := h.RecordAgentReceipt(t.Context(), agentReceiptCommand(a, 1, state)); err != nil {
				t.Fatal(err)
			}
			submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, Outcome: "failure", At: time.Now().UTC()})
			if err := h.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			m, _ = s.Get(m.ID)
			a = m.Actions[a.ID]
			if !a.Held() {
				t.Fatal("receipt alone resolved the unknown action")
			}
			r := submit(t, s, reviewCommand(m, a, "agent-review", "accepted", time.Now().UTC()))[0]
			if terminal {
				if r.Err != nil || !r.Allowed || r.Monitor.Actions[a.ID].Held() {
					t.Fatal("explicit terminal review rejected", r.Err)
				}
			} else if !errors.Is(r.Err, ErrExecutorUnfenced) {
				t.Fatal("local completion allowed review while remote work is active", r.Err)
			}
		})
	}
}

func TestAgentReceiptRejectsTerminalReplacementAndLateFailure(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 1, "TASK_STATE_FAILED")); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordAgentReceipt(context.Background(), agentReceiptCommand(a, 2, "TASK_STATE_COMPLETED")); !errors.Is(err, ErrAgentReceiptConflict) {
		t.Fatal("terminal evidence was overwritten", err)
	}
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "failure", Retryable: true})
	for _, outcome := range []string{"failure", "success"} {
		r := submit(t, s, Command{Kind: "late_result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: outcome, ExecutionStart: a.StartedAt, ExecutionEnd: a.StartedAt})[0]
		if !errors.Is(r.Err, ErrAgentReceiptConflict) {
			t.Fatal("failed task was converted to conclusive provider evidence", r.Err)
		}
	}
}

func TestAgentReceiptCommandAndImageValidation(t *testing.T) {
	s := openCatalogMemory(t)
	m, a, h := agentActionFixture(t, s, "intervention")
	r := agentReceiptCommand(a, 1, "TASK_STATE_WORKING")
	c := Command{Kind: "agent_receipt", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, ExecutorSession: a.ExecutorSession, At: r.Receipt.ObservedAt, AgentReceipt: &r}
	if err := validateCommand(c); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Command){
		"wrong kind":        func(c *Command) { c.Kind = "result" },
		"missing payload":   func(c *Command) { c.AgentReceipt = nil },
		"mixed payload":     func(c *Command) { c.Outcome = "success" },
		"oversized monitor": func(c *Command) { c.MonitorID = strings.Repeat("m", 257) },
		"oversized task":    func(c *Command) { c.AgentReceipt.Receipt.TaskID = strings.Repeat("t", 257) },
		"route suffix":      func(c *Command) { c.AgentReceipt.Receipt.TaskID = "task:subscribe" },
		"encoded route":     func(c *Command) { c.AgentReceipt.Receipt.TaskID = "task%2Fother" },
		"control character": func(c *Command) { c.AgentReceipt.Receipt.ContextID = "secret\tvalue" },
		"both reply types":  func(c *Command) { c.AgentReceipt.Receipt.MessageID = "reply" },
		"overflow":          func(c *Command) { c.AgentReceipt.ExpectedSequence, c.AgentReceipt.Receipt.Sequence = math.MaxUint64, 0 },
		"time mismatch":     func(c *Command) { c.At = c.At.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			copy, receipt := c, r
			copy.AgentReceipt = &receipt
			change(&copy)
			if err := validateCommand(copy); err == nil {
				t.Fatal("invalid receipt admitted")
			}
		})
	}
	for version := 1; version < AgentActionFormatVersion; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{c}})
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatal("old format admitted new fields", version)
		}
	}
	raw, _ := json.Marshal(envelope{Version: commandWriteFormat(c), Commands: []Command{c}})
	if _, err := decodeEnvelope(raw); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordAgentReceipt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	image, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, version := range []int{FormatVersion, CatalogFormatVersion, CollectionReselectionFormatVersion} {
		copy := image
		copy.Version = version
		if err := validateRecoveryReviewImage(copy); err == nil {
			t.Fatal("old image format retained new executor", version)
		}
	}
	before := ActionReviewRevision(image.Monitors[m.ID].Actions[a.ID])
	cloned := image.Monitors[m.ID].Actions[a.ID].Clone()
	cloned.AgentReceipt.State = "TASK_STATE_COMPLETED"
	if ActionReviewRevision(cloned) == before || reflect.DeepEqual(cloned.AgentReceipt, image.Monitors[m.ID].Actions[a.ID].AgentReceipt) {
		t.Fatal("review precondition or clone omitted agent receipt")
	}
	reserved := reservationCommand(t, s, "agent-mixed-command", time.Now().UTC())
	reserved.AgentReceipt = &r
	if _, _, err := operationDigest(reserved); !errors.Is(err, ErrOperationReservation) {
		t.Fatal("operation digest silently omitted agent receipt", err)
	}
}
