package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

func managedControlMonitor(t *testing.T, s *Store, id string) (Monitor, *CatalogGuard) {
	t.Helper()
	m := testMonitor()
	m.ID = id
	m.Policy.Intervention = true
	m.Policy.Unhealthy, m.Policy.Healthy = 1, 1
	m.Policy.Cooldown = 0
	m.Policy.Endpoints = map[string]int{"yellow": 2, "red": 2, "cyan": 2, "green": 2}
	record := createCatalog(t, s, catalogRecord(t, s, "Monitor", id, "uid-"+id, "resource-"+id, "{}"))
	guard := &CatalogGuard{Conditions: []CatalogCondition{{Key: record.Key, UID: record.UID, Revision: record.Revision}}}
	r := submit(t, s, Command{Kind: "configure", MonitorID: id, Revision: m.Revision, At: policyTime(), Config: &m, Guard: guard})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("configure", r.Err)
	}
	return *r.Monitor, guard
}
func controlPulse(t *testing.T, s *Store, m Monitor, g *CatalogGuard, at time.Time, outcome string) Monitor {
	t.Helper()
	r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, CheckControlRevision: m.ControlRevision, At: at, Generation: m.Generation + 1, Outcome: outcome, Guard: g})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal("pulse", r.Err)
	}
	return *r.Monitor
}
func controlRequest(m Monitor, action, revision string, at time.Time) Command {
	cc := &ControlCommand{Action: action, MonitorUID: m.CatalogUID, ExpectedRevision: m.ControlRevision, Revision: revision, OperationID: revision, Actor: "operator@example.test", Reason: "planned investigation", Note: "Checking upstream deployment"}
	if cc.Subject() == "incident" {
		cc.IncidentID, cc.ExpectedRevision = m.IncidentID, m.IncidentRevision
	}
	if action == "snooze" {
		cc.Until = at.Add(time.Hour)
	}
	return Command{Kind: "control", MonitorID: m.ID, At: at, Control: cc}
}
func applyControlRequest(t *testing.T, s *Store, c Command) Result {
	t.Helper()
	r := submit(t, s, c)[0]
	if r.Err != nil || !r.Allowed || r.Monitor == nil || r.Operation == nil {
		t.Fatalf("control %s: %+v", c.Control.Action, r)
	}
	return r
}

func TestControlsExactIncidentTriageAndDismissal(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "incident")
	m = controlPulse(t, s, m, g, policyTime().Add(time.Second), "failure")
	if m.IncidentID == "" || m.IncidentRevision == "" {
		t.Fatal("outage has no incident identity")
	}
	original := m.Clone()
	ack := controlRequest(m, "acknowledge", "ack-one", policyTime().Add(2*time.Second))
	ar := applyControlRequest(t, s, ack)
	m = *ar.Monitor
	if m.ControlRevision != original.ControlRevision || m.Revision != original.Revision || m.AcknowledgedBy != ack.Control.Actor || !reflect.DeepEqual(m.Actions, original.Actions) {
		t.Fatal("acknowledge changed execution or failed to record actor")
	}
	if r := submit(t, s, ack)[0]; !errors.Is(r.Err, ErrControlConflict) {
		t.Fatal("stale triage accepted", r.Err)
	}
	// A delivery already started cannot be retracted by dismissal.
	var started Action
	for _, a := range m.Actions {
		if a.Kind == "code" && a.Endpoint == 0 {
			started = a
			break
		}
	}
	sr := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: started.ID, At: policyTime().Add(3 * time.Second), Guard: g})[0]
	if !sr.Allowed {
		t.Fatal("start", sr.Err)
	}
	m = *sr.Monitor
	dr := applyControlRequest(t, s, controlRequest(m, "dismiss", "dismiss-one", policyTime().Add(4*time.Second)))
	m = *dr.Monitor
	for _, a := range m.Actions {
		switch {
		case a.ID == started.ID:
			if a.State != Started {
				t.Fatal("dismiss retracted started delivery")
			}
		case a.Kind == "code":
			if a.State != Cancelled || a.IncidentID != m.IncidentID {
				t.Fatal("unsent incident delivery escaped cancellation")
			}
		case a.Kind == "intervention":
			if a.State != Queued {
				t.Fatal("dismiss cancelled recovery")
			}
		}
	}
	if receipt, err := s.Operation(ar.Operation.ID); err != nil || receipt.Outcome != "superseded" {
		t.Fatal("prior triage receipt not superseded", receipt, err)
	}
	// Definite rejection of the already-started delivery is recorded but cannot retry a dismissed incident.
	rr := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: started.ID, At: policyTime().Add(5 * time.Second), Outcome: "failure", Retryable: true})[0]
	m = *rr.Monitor
	if m.Actions[started.ID].State != Failed {
		t.Fatal("actual rejection not retained")
	}
	before := m.Clone()
	m = *applyControlRequest(t, s, controlRequest(m, "reopen", "reopen-one", policyTime().Add(6*time.Second))).Monitor
	if m.Dismissed || !reflect.DeepEqual(m.Actions, before.Actions) || m.InterventionAttempts != before.InterventionAttempts {
		t.Fatal("reopen replayed prior actions or reset recovery budget")
	}
	if !s.Status().Ready {
		t.Fatal("ordinary control conflict failed storage")
	}
}

func TestControlsSnoozeFencesQueuedChecksAndRetainsStartedResults(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "pause")
	m = controlPulse(t, s, m, g, policyTime().Add(time.Second), "failure")
	oldVersion := m.ControlRevision
	a := interventionAction(t, m)
	sr := submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: policyTime().Add(2 * time.Second), Guard: g})[0]
	m = *sr.Monitor
	at := policyTime().Add(3 * time.Second)
	m = *applyControlRequest(t, s, controlRequest(m, "snooze", "snooze-one", at)).Monitor
	for _, a := range m.Actions {
		if a.State == Queued {
			t.Fatal("snooze retained unsent work")
		}
	}
	if err := s.CheckCheckAdmission(m.ID, g, oldVersion, at); !errors.Is(err, ErrControlConflict) {
		t.Fatal("old dispatch accepted during pause", err)
	}
	if err := s.CheckCatalogGuard(m.ID, g); err != nil {
		t.Fatal("dependency proof incorrectly includes pause", err)
	}
	if r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at, Outcome: "failure", Guard: g, CheckControlRevision: oldVersion})[0]; r.Monitor != nil {
		t.Fatal("unstarted paused pulse changed state")
	}
	prior := m.Clone()
	r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: m.Generation + 1, At: at.Add(time.Second), Outcome: "failure", Guard: g, CheckControlRevision: oldVersion, ExecutionStart: at.Add(-time.Second), ExecutionEnd: at})[0]
	m = *r.Monitor
	if m.TotalChecks != prior.TotalChecks+1 || m.NextCheck != prior.NextCheck || m.PulseFailures != prior.PulseFailures || !reflect.DeepEqual(m.Actions, prior.Actions) {
		t.Fatal("started check observation lost or produced new work")
	}
	r = submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), Outcome: "success"})[0]
	m = *r.Monitor
	if m.Actions[a.ID].State != Succeeded {
		t.Fatal("started recovery result lost")
	}
	for _, a := range m.Actions {
		if a.State == Queued {
			t.Fatal("recovery outcome queued work during snooze")
		}
	}
	m = *applyControlRequest(t, s, controlRequest(m, "unsnooze", "resume-one", at.Add(3*time.Second))).Monitor
	if err := s.CheckCheckAdmission(m.ID, g, oldVersion, at.Add(4*time.Second)); !errors.Is(err, ErrControlConflict) {
		t.Fatal("pre-pause check revived after resume", err)
	}
	if err := s.CheckCheckAdmission(m.ID, g, m.ControlRevision, at.Add(4*time.Second)); err != nil {
		t.Fatal("fresh post-resume dispatch rejected", err)
	}
}

func TestControlsExpiryAndDisableRemainIndependent(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "expiry")
	at := policyTime().Add(time.Second)
	m = *applyControlRequest(t, s, controlRequest(m, "snooze", "short-pause", at)).Monitor
	firstUntil, firstVersion := m.SnoozedUntil, m.ControlRevision
	c := controlRequest(m, "snooze", "extended-pause", at.Add(time.Minute))
	c.Control.Until = firstUntil.Add(time.Hour)
	m = *applyControlRequest(t, s, c).Monitor
	expiry := controlRequest(m, "expire_snooze", "expired-old", firstUntil)
	expiry.Control.ExpectedRevision, expiry.Control.Until, expiry.Control.Actor = firstVersion, firstUntil, "system"
	if r := submit(t, s, expiry)[0]; !errors.Is(r.Err, ErrControlConflict) {
		t.Fatal("stale expiry cleared extension", r.Err)
	}
	config := m.Clone()
	config.Policy.Enabled = false
	r := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: at.Add(2 * time.Minute), Config: &config, Guard: g})[0]
	m = *r.Monitor
	expiry = controlRequest(m, "expire_snooze", "expired-current", m.SnoozedUntil)
	expiry.Control.Until, expiry.Control.Actor = m.SnoozedUntil, "system"
	m = *applyControlRequest(t, s, expiry).Monitor
	if m.Policy.Enabled || !m.SnoozedUntil.IsZero() {
		t.Fatal("expiry enabled a disabled monitor")
	}
	if err := s.CheckCheckAdmission(m.ID, g, m.ControlRevision, expiry.At); !errors.Is(err, ErrControlConflict) {
		t.Fatal("disabled health check admitted")
	}
}

func TestControlsReceiptSubjectAndFrozenIndexes(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "pages-a")
	m = controlPulse(t, s, m, g, policyTime().Add(time.Second), "failure")
	view, err := s.IncidentSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	controlView, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	r := applyControlRequest(t, s, controlRequest(m, "acknowledge", "page-ack", policyTime().Add(2*time.Second)))
	m = *r.Monitor
	if old, ok := view.Get(m.IncidentID); !ok || old.AcknowledgedBy != "" {
		t.Fatal("frozen incident view changed")
	}
	if old, ok := view.ForMonitor(m.ID); !ok || old.Revision == m.IncidentRevision {
		t.Fatal("monitor incident index changed")
	}
	receipt := r.Operation
	update := OperationUpdate{ID: receipt.ID, Key: receipt.Key, UID: receipt.UID, Revision: receipt.NewVersion, Applied: true}
	if r := submit(t, s, Command{Kind: "operation", At: policyTime().Add(3 * time.Second), Operation: &update})[0]; !errors.Is(r.Err, ErrCatalogConflict) {
		t.Fatal("configuration completion claimed incident projection", r.Err)
	}
	update.Subject, update.IncidentID = receipt.Subject, receipt.IncidentID
	if r := submit(t, s, Command{Kind: "operation", At: policyTime().Add(3 * time.Second), Operation: &update})[0]; r.Err != nil || r.Operation.State != "completed" {
		t.Fatal("exact incident projection not completed", r.Err)
	}
	changes, err := s.ControlsChangedSince(controlView.Cursor, 100)
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].IncidentRevision != m.IncidentRevision {
		t.Fatal("bounded change stream missed triage", changes, err)
	}
	// A separate monitor changes the live tree, while the old page remains one record.
	other, og := managedControlMonitor(t, s, "pages-b")
	_ = controlPulse(t, s, other, og, policyTime().Add(time.Second), "failure")
	rows, next, err := view.Page("", 1)
	if err != nil || len(rows) != 1 || next != "" {
		t.Fatal("frozen page changed", rows, next, err)
	}
	fresh, _ := s.IncidentSnapshot()
	rows, next, err = fresh.Page("", 1)
	if err != nil || len(rows) != 1 || next == "" {
		t.Fatal("bounded page missing continuation")
	}
	tail, more, err := fresh.Page(next, 1)
	if err != nil || len(tail) != 1 || more != "" || tail[0].ID == rows[0].ID {
		t.Fatal("incident pagination duplicate/loss")
	}
}

func TestControlsSnapshotRecoveryValidationAndLegacyAdoption(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "snapshot")
	m = controlPulse(t, s, m, g, policyTime().Add(time.Second), "failure")
	m = *applyControlRequest(t, s, controlRequest(m, "dismiss", "snap-dismiss", policyTime().Add(2*time.Second))).Monitor
	m = *applyControlRequest(t, s, controlRequest(m, "snooze", "snap-pause", policyTime().Add(3*time.Second))).Monitor
	snap, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	data, err := json.Marshal(snap.(*frozenSnapshot).image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImage(bytes.NewReader(data)); err != nil {
		t.Fatal("new snapshot invalid", err)
	}
	f := &machine{history: s.fsm.history}
	if err := f.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	restored := f.image.Monitors[m.ID]
	if restored.ControlRevision != m.ControlRevision || restored.IncidentRevision != m.IncidentRevision || !restored.Dismissed || restored.SnoozedUntil != m.SnoozedUntil {
		t.Fatal("control state did not survive snapshot")
	}
	legacy := testMonitor()
	legacy.Incident, legacy.Recovering = true, true
	imageData, err := json.Marshal(image{Version: FormatVersion, Monitors: map[string]Monitor{legacy.ID: legacy}})
	if err != nil {
		t.Fatal(err)
	}
	lf := &machine{history: s.fsm.history}
	if err := lf.Restore(io.NopCloser(bytes.NewReader(imageData))); err != nil {
		t.Fatal(err)
	}
	adopted := lf.image.Monitors[legacy.ID]
	if adopted.IncidentID == "" || adopted.ControlRevision == "" || !adopted.IncidentOpenedAt.IsZero() {
		t.Fatal("legacy adoption invented time or lost identity")
	}
	lf2 := &machine{history: s.fsm.history}
	_ = lf2.Restore(io.NopCloser(bytes.NewReader(imageData)))
	if lf2.image.Monitors[legacy.ID].IncidentID != adopted.IncidentID {
		t.Fatal("legacy identity nondeterministic")
	}
	bad := snap.(*frozenSnapshot).image
	copy := bad.Monitors[m.ID]
	copy.DismissedBy = ""
	bad.Monitors[m.ID] = copy
	data, _ = json.Marshal(bad)
	if _, err := decodeImage(bytes.NewReader(data)); err == nil {
		t.Fatal("corrupt dismissal accepted")
	}
}

func TestControlsRejectInvalidPayloadBeforeCommit(t *testing.T) {
	m := Monitor{CatalogUID: "uid", ControlRevision: "before"}
	at := policyTime()
	base := controlRequest(m, "snooze", "after", at)
	for name, mutate := range map[string]func(*Command){"negative": func(c *Command) { c.Control.Until = at }, "over_maximum": func(c *Command) { c.Control.Until = at.Add(MaxSnoozeDuration + time.Nanosecond) }, "no_reason": func(c *Command) { c.Control.Reason = " " }, "actor_injection": func(c *Command) { c.Control.Actor = "actor\nforged" }, "extra_data": func(c *Command) { c.Driver = "secret-diagnostic" }} {
		t.Run(name, func(t *testing.T) {
			c := base
			copy := *base.Control
			c.Control = &copy
			c.MonitorID = "monitor"
			mutate(&c)
			if !errors.Is(validateCommand(c), ErrControlInvalid) {
				t.Fatal("invalid control accepted")
			}
		})
	}
}

func TestControlsRealRaftRestart(t *testing.T) {
	ctx := context.Background()
	config := testConfig(t)
	s, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	m, g := managedControlMonitor(t, s, "restart")
	m = controlPulse(t, s, m, g, time.Now().UTC(), "failure")
	m = *applyControlRequest(t, s, controlRequest(m, "acknowledge", "restart-ack", time.Now().UTC())).Monitor
	m = *applyControlRequest(t, s, controlRequest(m, "snooze", "restart-pause", time.Now().UTC())).Monitor
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, ok, err := s.MonitorStatus(m.ID)
	if err != nil || !ok || status.ControlRevision != m.ControlRevision || status.SnoozedUntil != m.SnoozedUntil {
		t.Fatal("restart lost controls", status, err)
	}
	incident, ok, err := s.Incident(m.IncidentID)
	if err != nil || !ok || incident.AcknowledgedBy == "" {
		t.Fatal("restart lost triage", incident, err)
	}
	receipt, err := s.Operation("restart-pause")
	if err != nil || receipt.Subject != "control" || receipt.State != "committed" {
		t.Fatal("restart lost owner receipt", receipt, err)
	}
}

func TestControlChangeGapIsExplicit(t *testing.T) {
	s := openCatalogMemory(t)
	m, _ := managedControlMonitor(t, s, "gap")
	view, _ := s.ControlSnapshot()
	// Exercise ring bookkeeping under its owning lock without generating 8K fsyncs.
	s.fsm.mu.Lock()
	for n := 0; n <= controlChangeCapacity; n++ {
		next := m.Clone()
		next.ControlRevision = fmt.Sprintf("control-%d", n)
		s.fsm.installMonitor(m, next, policyTime())
		m = next
	}
	s.fsm.mu.Unlock()
	if _, err := s.ControlsChangedSince(view.Cursor, 500); !errors.Is(err, ErrControlChangeGap) {
		t.Fatal("lost control updates not reported", err)
	}
	fresh, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := fresh.Page("", 500)
	if err != nil || len(rows) != 1 || rows[0].ControlRevision != m.ControlRevision {
		t.Fatal("gap recovery has stale projection")
	}
}

func TestControlsIncidentClosureAndNewIncidentNeverReplaysDismissedWork(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := managedControlMonitor(t, s, "closure")
	m.Policy.Intervention = false
	r := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: policyTime(), Config: &m, Guard: g})[0]
	m = *r.Monitor
	m = controlPulse(t, s, m, g, policyTime().Add(time.Second), "failure")
	incident := m.IncidentID
	m = *applyControlRequest(t, s, controlRequest(m, "dismiss", "closure-dismiss", policyTime().Add(2*time.Second))).Monitor
	m = controlPulse(t, s, m, g, policyTime().Add(3*time.Second), "success")
	if m.IncidentClosedAt.IsZero() {
		t.Fatal("recovered incident lacks closure time")
	}
	for _, a := range m.Actions {
		if a.Color == "green" && a.State == Queued {
			t.Fatal("dismissed incident sent closing green")
		}
	}
	c := controlRequest(m, "reopen", "reopen-closed", policyTime().Add(4*time.Second))
	if r := submit(t, s, c)[0]; !errors.Is(r.Err, ErrIncidentNotActive) {
		t.Fatal("closed incident reopened", r.Err)
	}
	m = controlPulse(t, s, m, g, policyTime().Add(5*time.Second), "failure")
	if m.IncidentID == incident || m.Dismissed || m.AcknowledgedBy != "" {
		t.Fatal("new incident inherited prior triage")
	}
	m = controlPulse(t, s, m, g, policyTime().Add(6*time.Second), "success")
	for _, a := range m.Actions {
		if a.Color == "green" && a.State == Queued && a.IncidentID == m.IncidentID {
			return
		}
	}
	t.Fatal("closing green does not identify its exact incident")
}

func TestControlsCatalogRemovalSupersedesPendingBeforeOwnerProjection(t *testing.T) {
	s := openCatalogMemory(t)
	m, _ := managedControlMonitor(t, s, "deleted")
	m = *applyControlRequest(t, s, controlRequest(m, "snooze", "pending-removal", policyTime().Add(time.Second))).Monitor
	old := requireCatalog(t, s, CatalogKey{Kind: "Monitor", ID: m.ID})
	mutation := deleteCatalogMutation(old, "removed-version")
	if r := catalogSubmit(t, s, mutation); r.Err != nil || !r.Allowed {
		t.Fatal("remove", r.Err)
	}
	receipt, err := s.Operation("pending-removal")
	if err != nil || receipt.Outcome != "superseded" {
		t.Fatal("removed subject retained pending operation", receipt, err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	data, _ := json.Marshal(snapshot.(*frozenSnapshot).image)
	if _, err := decodeImage(bytes.NewReader(data)); err != nil {
		t.Fatal("snapshot before owner deletion invalid", err)
	}
}
