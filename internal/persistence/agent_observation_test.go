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

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func agentObservationSealer(t *testing.T) *secureconfig.Sealer {
	t.Helper()
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{49}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

const agentObservationSecret = `{"url":"https://original-agent.example","token":"original-agent-private-token"}`

func agentObservationFixture(t *testing.T, s *Store, kind string) (*LocalExecution, ActionRecord) {
	t.Helper()
	m := testMonitor()
	m.Policy.Intervention = kind == "intervention"
	m.Policy.Endpoints = map[string]int{"red": 1}
	at := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})
	m = *submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at})[0].Monitor
	for _, action := range m.Actions {
		if action.Kind != kind {
			continue
		}
		connection, err := agentObservationSealer(t).Seal(t.Context(), AgentConnectionBinding(s.Status().NodeID, m.ID, action), []byte(agentObservationSecret))
		if err != nil {
			t.Fatal(err)
		}
		h, err := s.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision,
			ActionID: action.ID, Driver: "a2a", At: at, AgentConnection: &AgentConnection{CatalogUID: action.CatalogUID, Payload: connection}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.Finish(context.Background()) })
		action.StartedAt = at
		if err := h.RecordAgentReceipt(t.Context(), agentReceiptCommand(action, 1, "TASK_STATE_WORKING")); err != nil {
			t.Fatal(err)
		}
		submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID,
			Outcome: "failure", Ambiguous: true, At: at.Add(time.Second)})
		record, ok, err := s.Action(action.ID)
		if err != nil || !ok {
			t.Fatal("action missing", err)
		}
		return h, record
	}
	t.Fatal("action not queued")
	return nil, ActionRecord{}
}

func nextAgentObservation(a ActionRecord, state string) AgentReceipt {
	r := *a.AgentReceipt
	r.Sequence++
	r.State = state
	r.ObservedAt = a.StartedAt.Add(2 * time.Second)
	return r
}

func TestAgentObservationRetainsUnknownOutcomeAndReview(t *testing.T) {
	for _, kind := range []string{"intervention", "code"} {
		t.Run(kind, func(t *testing.T) {
			s := openCatalogMemory(t)
			h, a := agentObservationFixture(t, s, kind)
			observation := nextAgentObservation(a, "TASK_STATE_COMPLETED")
			if err := s.RecordAgentObservation(t.Context(), a, observation); !errors.Is(err, ErrExecutorUnfenced) {
				t.Fatal("observation stole authority from a live invocation", err)
			}
			if err := h.Finish(t.Context()); err != nil {
				t.Fatal(err)
			}
			a, _, _ = s.Action(a.ID)
			before, _ := s.Get(a.MonitorID)
			if a.ExecutorFenced || !a.Held {
				t.Fatal("local return fenced remote work")
			}
			if err := s.RecordAgentObservation(t.Context(), a, observation); err != nil {
				t.Fatal(err)
			}
			after, _, _ := s.Action(a.ID)
			m, _ := s.Get(a.MonitorID)
			if after.State != Unknown || !after.Held || !after.ExecutorFenced || after.Review != nil ||
				after.Outcome != a.Outcome || !after.FinishedAt.Equal(a.FinishedAt) || after.ReviewRevision == a.ReviewRevision {
				t.Fatal("observation resolved the action or lost the review precondition")
			}
			if before.ActionProjection != nil && before.ActionProjection.LatestRecovery != nil && before.ActionProjection.LatestRecovery.ID == a.ID {
				updated := after.Action.Clone()
				before.ActionProjection.LatestRecovery = &updated
			}
			if _, exists := before.Actions[a.ID]; exists {
				before.Actions[a.ID] = after.Action.Clone()
			}
			if !reflect.DeepEqual(before, m) {
				t.Fatal("observation changed incident, scheduling or action admission")
			}
			page, _ := s.History().Page(a.MonitorID, "", 100)
			if err := s.RecordAgentObservation(t.Context(), a, observation); err != nil {
				t.Fatal("identical retry failed", err)
			}
			repeated, _ := s.History().Page(a.MonitorID, "", 100)
			if len(page.Events) != len(repeated.Events) {
				t.Fatal("retry duplicated history")
			}
			changed := nextAgentObservation(after, "TASK_STATE_FAILED")
			if err := s.RecordAgentObservation(t.Context(), after, changed); !errors.Is(err, ErrAgentReceiptConflict) {
				t.Fatal("terminal evidence overwritten", err)
			}
		})
	}
}

func TestAgentObservationRejectsStaleOrSubstitutedAuthority(t *testing.T) {
	s := openCatalogMemory(t)
	h, a := agentObservationFixture(t, s, "intervention")
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Action(a.ID)
	for name, alter := range map[string]func(*ActionRecord, *AgentReceipt){
		"action":               func(a *ActionRecord, _ *AgentReceipt) { a.ID = "another-action" },
		"monitor":              func(a *ActionRecord, _ *AgentReceipt) { a.MonitorID = "another-monitor" },
		"execution revision":   func(a *ActionRecord, _ *AgentReceipt) { a.Revision = "another-revision" },
		"review revision":      func(a *ActionRecord, _ *AgentReceipt) { a.ReviewRevision = identity("another-review") },
		"task":                 func(_ *ActionRecord, r *AgentReceipt) { r.TaskID = "another-task" },
		"context":              func(_ *ActionRecord, r *AgentReceipt) { r.ContextID = "another-context" },
		"observation sequence": func(_ *ActionRecord, r *AgentReceipt) { r.Sequence++ },
		"observation time":     func(a *ActionRecord, r *AgentReceipt) { r.ObservedAt = a.StartedAt },
		"message":              func(_ *ActionRecord, r *AgentReceipt) { r.TaskID, r.State, r.MessageID = "", "", "message" },
	} {
		t.Run(name, func(t *testing.T) {
			original, receipt := a, nextAgentObservation(a, "TASK_STATE_COMPLETED")
			alter(&original, &receipt)
			if err := s.RecordAgentObservation(t.Context(), original, receipt); err == nil {
				t.Fatal("invalid observation admitted")
			}
		})
	}
	working := nextAgentObservation(a, "TASK_STATE_WORKING")
	if err := s.RecordAgentObservation(t.Context(), a, working); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentObservation(t.Context(), a, nextAgentObservation(a, "TASK_STATE_COMPLETED")); !errors.Is(err, ErrAgentReceiptConflict) {
		t.Fatal("concurrent observation overwrote a committed observation", err)
	}
	current, _, _ := s.Action(a.ID)
	if current.ExecutorFenced || !current.Held || !sameAgentReceipt(*current.AgentReceipt, working) {
		t.Fatal("nonterminal observation released the hold")
	}
}

func TestAgentConnectionBindsOriginalActionAndSurvivesConfigurationRemoval(t *testing.T) {
	s := openCatalogMemory(t)
	h, a := agentObservationFixture(t, s, "intervention")
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Action(a.ID)
	sealer := agentObservationSealer(t)
	open := func(storeID, monitorID string, action Action) ([]byte, error) {
		return sealer.Open(t.Context(), AgentConnectionBinding(storeID, monitorID, action), a.AgentConnection.Payload)
	}
	for name, change := range map[string]func(*Action){
		"action": func(a *Action) { a.ID = "different" },

		"revision": func(a *Action) { a.Revision = "different" },
		"incident": func(a *Action) { a.IncidentID = "different" },
		"kind":     func(a *Action) { a.Kind = "code" },
		"color":    func(a *Action) { a.Color = "green" },
		"endpoint": func(a *Action) { a.Endpoint++ },
		"attempt":  func(a *Action) { a.Attempt++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := a.Action.Clone()
			change(&changed)
			if _, err := open(s.Status().NodeID, a.MonitorID, changed); err == nil {
				t.Fatal("connection substituted into a different action")
			}
		})
	}
	if _, err := open("another-store", a.MonitorID, a.Action); err == nil {
		t.Fatal("connection accepted in another store")
	}
	if _, err := open(s.Status().NodeID, "another-monitor", a.Action); err == nil {
		t.Fatal("connection accepted for another monitor")
	}
	submit(t, s, Command{Kind: "remove", MonitorID: a.MonitorID, Revision: a.Revision, At: time.Now().UTC()})
	m := testMonitor()
	m.Revision = "new-configuration"
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: time.Now().UTC()})
	original, _, _ := s.Action(a.ID)
	if original.CatalogUID != a.CatalogUID || original.Revision != a.Revision || original.ReviewRevision != a.ReviewRevision {
		t.Fatal("configuration adoption rewrote the original connection identity")
	}
	if err := s.RecordAgentObservation(t.Context(), original, nextAgentObservation(original, "TASK_STATE_COMPLETED")); err != nil {
		t.Fatal("original observation lost after removal or recreation", err)
	}
	observed, _, _ := s.Action(a.ID)
	plain, err := open(s.Status().NodeID, a.MonitorID, observed.Action)
	defer clear(plain)
	if err != nil || string(plain) != agentObservationSecret {
		t.Fatal("original connection lost after lifecycle/configuration changes", err)
	}
}

func TestAgentConnectionSnapshotAndReadsAreDetached(t *testing.T) {
	s := openCatalogMemory(t)
	h, a := agentObservationFixture(t, s, "intervention")
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	a, _, _ = s.Action(a.ID)
	read, _, _ := s.Action(a.ID)
	read.AgentConnection.Payload.Ciphertext[0] ^= 1
	view, _ := s.ActionSnapshot()
	fromView, _, _ := view.Get(a.ID)
	fromView.AgentConnection.Payload.Nonce[0] ^= 1
	m, _ := s.Get(a.MonitorID)
	if _, exists := m.Actions[a.ID]; exists {
		t.Fatal("completed action entered hot projection")
	}
	fromView.AgentConnection.Payload.WrappedKey[0] ^= 1
	defer view.Close()
	if err := s.RecordAgentObservation(t.Context(), a, nextAgentObservation(a, "TASK_STATE_COMPLETED")); err != nil {
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
	retained := snapshotAction(t, ledger, a.ID)
	if image.Version != DispatchFormatVersion || retained.AgentReceipt.State != "TASK_STATE_WORKING" {
		t.Fatal("snapshot was not frozen before the later observation")
	}
	plain, err := agentObservationSealer(t).Open(t.Context(), AgentConnectionBinding(s.Status().NodeID, a.MonitorID, retained), retained.AgentConnection.Payload)
	defer clear(plain)
	if err != nil || string(plain) != agentObservationSecret {
		t.Fatal("read or later mutation damaged snapshot connection", err)
	}
	if bytes.Contains(sink.Bytes(), []byte("original-agent.example")) || bytes.Contains(sink.Bytes(), []byte("original-agent-private-token")) {
		t.Fatal("snapshot leaked connection")
	}
	page, _ := s.History().Page(a.MonitorID, "", 100)
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, []byte("agent_connection")) || bytes.Contains(raw, []byte(a.AgentConnection.Payload.KeyID)) {
		t.Fatal("history retained connection material")
	}
}

func TestAgentObservationCommandAndSnapshotBounds(t *testing.T) {
	s := openCatalogMemory(t)
	h, a := agentObservationFixture(t, s, "intervention")
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.Action(a.ID)
	r := nextAgentObservation(a, "TASK_STATE_COMPLETED")
	command := Command{Kind: "agent_observation", MonitorID: a.MonitorID, Revision: a.Revision, ActionID: a.ID,
		ExecutorSession: s.executorSession, At: r.ObservedAt,
		AgentObservation: &AgentObservationCommand{ExpectedRevision: a.ReviewRevision, ExpectedSequence: a.AgentReceipt.Sequence, Receipt: r}}
	if err := validateCommand(command); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*Command){
		"wrong kind":             func(c *Command) { c.Kind = "result" },
		"no payload":             func(c *Command) { c.AgentObservation = nil },
		"success":                func(c *Command) { c.Outcome = "success" },
		"connection replacement": func(c *Command) { c.AgentConnection = a.AgentConnection },
		"invocation receipt":     func(c *Command) { c.AgentReceipt = &AgentReceiptCommand{} },
		"configuration guard":    func(c *Command) { c.Guard = &CatalogGuard{} },
		"oversized monitor":      func(c *Command) { c.MonitorID = strings.Repeat("m", 257) },
		"bad digest":             func(c *Command) { c.AgentObservation.ExpectedRevision = strings.Repeat("z", 64) },
		"overflow": func(c *Command) {
			c.AgentObservation.ExpectedSequence = math.MaxUint64
			c.AgentObservation.Receipt.Sequence = 0
		},
		"clock mismatch": func(c *Command) { c.At = c.At.Add(time.Second) },
		"task URL":       func(c *Command) { c.AgentObservation.Receipt.TaskID = "https://agent/tasks/id" },
	} {
		t.Run(name, func(t *testing.T) {
			copy, payload := command, *command.AgentObservation
			copy.AgentObservation = &payload
			alter(&copy)
			if validateCommand(copy) == nil {
				t.Fatal("invalid observation admitted")
			}
		})
	}
	for version := 1; version < AgentObservationFormatVersion; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		if _, err := decodeEnvelope(raw); err == nil {
			t.Fatal("old format accepted new observation authority", version)
		}
	}
	image, ledger, err := decodeSnapshot(bytes.NewReader(captureSnapshotBytes(t, s.fsm)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	image.Monitors[a.MonitorID] = withAction(image.Monitors[a.MonitorID], snapshotAction(t, ledger, a.ID))
	image.Version = AgentActionFormatVersion
	if validateRecoveryReviewImage(image) == nil {
		t.Fatal("old snapshot version accepted connection payload")
	}
	image.Version = AgentObservationFormatVersion
	bad := image.Monitors[a.MonitorID].Actions[a.ID]
	bad.AgentConnection.CatalogUID = "another-incarnation"
	if validateRecoveryReviewImage(image) == nil {
		t.Fatal("connection from another incarnation accepted")
	}
	for _, size := range []int{0, AgentConnectionMaxBytes + 17} {
		connection := a.AgentConnection.Clone()
		connection.Payload.Ciphertext = make([]byte, size)
		start := Command{Kind: "start", MonitorID: a.MonitorID, Revision: a.Revision, ActionID: a.ID,
			ExecutorSession: s.executorSession, Driver: "a2a", At: r.ObservedAt, AgentConnection: &connection}
		if validateCommand(start) == nil {
			t.Fatal("unbounded or empty encrypted connection admitted")
		}
	}
}

func TestAgentConnectionPreservesManifestAdoption(t *testing.T) {
	s := openCatalogMemory(t)
	h, a := agentObservationFixture(t, s, "intervention")
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(a.MonitorID)
	configured := m.Clone()
	configured.CatalogUID = "adopted-incarnation"
	// The pure transition receives a configuration whose catalog guard was
	// already checked by Apply. Test adoption without manufacturing a catalog.
	result := transition(m, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &configured, At: time.Now().UTC()})
	adopted := retainedAction(t, s, a.ID)
	if result.Monitor.AdoptedFileIncarnation == nil || *result.Monitor.AdoptedFileIncarnation != adopted.FileIncarnation || !result.Monitor.OwnsAction(adopted) || adopted.CatalogUID != "" || !adopted.Held() || !result.Monitor.otherActiveRecovery("new-action") {
		t.Fatal("adoption released or orphaned an unknown intervention")
	}
	plain, err := agentObservationSealer(t).Open(t.Context(), AgentConnectionBinding(s.Status().NodeID, m.ID, adopted), adopted.AgentConnection.Payload)
	defer clear(plain)
	if err != nil || string(plain) != agentObservationSecret {
		t.Fatal("adoption redirected or lost the original connection", err)
	}
	bound := adopted.Clone()
	bound.CatalogUID = "replacement-incarnation"
	bound.AgentConnection.CatalogUID = bound.CatalogUID
	if _, err := agentObservationSealer(t).Open(t.Context(), AgentConnectionBinding(s.Status().NodeID, m.ID, bound), bound.AgentConnection.Payload); err == nil {
		t.Fatal("cleartext source incarnation replaced the authenticated origin")
	}
}

func TestAgentObservationPreservesConcurrentOperatorReview(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := manualFixture(t, s)
	at := time.Now().UTC()
	m = *mustResult(t, s, manualCommand(m, guard, "observed-manual", at)).Monitor
	a := latestManual(t, m)
	payload, err := agentObservationSealer(t).Seal(t.Context(), AgentConnectionBinding(s.Status().NodeID, m.ID, a), []byte(agentObservationSecret))
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.BeginLocalAction(t.Context(), Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID,
		Guard: guard, Driver: "a2a", At: at, AgentConnection: &AgentConnection{CatalogUID: a.CatalogUID, Payload: payload}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Finish(context.Background()) })
	a.StartedAt = at
	if err := h.RecordAgentReceipt(t.Context(), agentReceiptCommand(a, 1, "TASK_STATE_WORKING")); err != nil {
		t.Fatal(err)
	}
	unknown := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(time.Second), Outcome: "failure", Ambiguous: true})[0]
	if unknown.Err != nil || unknown.Monitor == nil || unknown.Monitor.Actions[a.ID].State != Unknown {
		t.Fatal("ambiguous result did not preserve unknown state", unknown.Err)
	}
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	original, _, _ := s.Action(a.ID)
	m, _ = s.Get(m.ID)
	review := reviewCommand(m, original.Action, "observed-review", "inconclusive", at.Add(2*time.Second))
	reviewed := mustResult(t, s, review)
	receipt := nextAgentObservation(original, "TASK_STATE_COMPLETED")
	receipt.ObservedAt = at.Add(3 * time.Second)
	if err := s.RecordAgentObservation(t.Context(), original, receipt); !errors.Is(err, ErrAgentReceiptConflict) {
		t.Fatal("an observation based on an older review was accepted", err)
	}
	current, _, _ := s.Action(a.ID)
	if err := s.RecordAgentObservation(t.Context(), current, receipt); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Action(a.ID)
	if !reflect.DeepEqual(after.Review, reviewed.Monitor.Actions[a.ID].Review) || !after.Held || after.State != Unknown || !after.ExecutorFenced {
		t.Fatal("task completion changed the operator assertion or released its hold")
	}
	// A fresh explicit review may resolve the hold only after terminal evidence.
	m, _ = s.Get(m.ID)
	resolved := mustResult(t, s, reviewCommand(m, after.Action, "observed-final-review", "accepted", at.Add(4*time.Second)))
	if resolved.Monitor.Actions[a.ID].Held() {
		t.Fatal("fresh review could not resolve the stopped remote action")
	}
	if err := s.RecordAgentObservation(t.Context(), current, receipt); err != nil {
		t.Fatal("an identical receipt retry failed after review", err)
	}
	final, _, _ := s.Action(a.ID)
	if final.Held || final.Review.Resolution != "accepted" {
		t.Fatal("an identical receipt retry replaced a later review")
	}
}
