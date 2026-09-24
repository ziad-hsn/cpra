package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func prepareGuardFixture(t *testing.T, s *Store) (Monitor, CatalogRecord, CatalogRecord, *CatalogGuard) {
	t.Helper()
	m := testMonitor()
	secret := createCatalog(t, s, catalogRecord(t, s, "Credential", "guard-key", "key-uid", "key-v1", "private-secret"))
	endpoint := createCatalog(t, s, catalogRecord(t, s, "NotificationEndpoint", "guard-endpoint", "endpoint-uid", "endpoint-v1", "private-destination", secret.Key))
	monitor := createCatalog(t, s, catalogRecord(t, s, "Monitor", m.ID, "monitor-uid", "monitor-v1", "private-monitor", endpoint.Key))
	guard := &CatalogGuard{Conditions: catalogConditions(t, s, []CatalogKey{monitor.Key, endpoint.Key, secret.Key})}
	return m, monitor, secret, guard
}

func TestExecutionGuardRequiresCompleteCurrentClosure(t *testing.T) {
	s := openCatalogMemory(t)
	m, monitor, secret, guard := prepareGuardFixture(t, s)
	now := time.Now().UTC()
	command := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: now}
	if result := submit(t, s, command)[0]; !errors.Is(result.Err, ErrCatalogDependency) || result.Allowed {
		t.Fatal("unguarded legacy configuration replaced a managed monitor", result.Err)
	}
	partial := guard.Clone()
	partial.Conditions = partial.Conditions[:2]
	command.Guard = &partial
	if result := submit(t, s, command)[0]; !errors.Is(result.Err, ErrCatalogDependency) {
		t.Fatal("omitted transitive secret was accepted", result.Err)
	}
	command.Guard = guard
	if result := submit(t, s, command)[0]; result.Err != nil || !result.Allowed {
		t.Fatal("current complete preparation rejected", result.Err)
	}
	if err := s.CheckCatalogGuard(m.ID, guard); err != nil {
		t.Fatal(err)
	}
	// A dependency change ordered before configuration/start must win even
	// though the Monitor resource version and executable content hash match.
	secret = requireCatalog(t, s, secret.Key)
	change := updateCatalogMutation(t, s, secret, "key-v2", "rotated-secret")
	results := submit(t, s, Command{Kind: "catalog", Catalog: &change, At: change.Record.UpdatedAt}, command)
	if results[0].Err != nil || !errors.Is(results[1].Err, ErrCatalogDependency) {
		t.Fatal("same-batch dependency ordering lost", results[0].Err, results[1].Err)
	}
	if err := s.CheckCatalogGuard(m.ID, guard); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("health worker could invoke a stale private job", err)
	}
	if current := requireCatalog(t, s, monitor.Key); current.Revision != monitor.Revision {
		t.Fatal("fixture unexpectedly changed root revision")
	}
	if _, err := s.CatalogSnapshot(); err != nil {
		t.Fatal("normal guard conflict poisoned persistence", err)
	}
}

func TestExecutionGuardStartAndKnownOutcomeBoundary(t *testing.T) {
	s := openCatalogMemory(t)
	m, _, secret, guard := prepareGuardFixture(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: guard, At: now})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: guard, Generation: 1, At: now, Outcome: "failure"})
	m, _ = s.Get(m.ID)
	ids := sortedActions(m.Actions)
	if len(ids) != 2 {
		t.Fatal("fixture did not open queued actions")
	}
	start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, Guard: guard, ActionID: ids[0], At: now}
	if r := submit(t, s, start)[0]; !r.Allowed || r.Err != nil {
		t.Fatal("fresh start denied", r.Err)
	}
	secret = requireCatalog(t, s, secret.Key)
	if r := catalogSubmit(t, s, updateCatalogMutation(t, s, secret, "key-v2", "rotated")); r.Err != nil {
		t.Fatal(r.Err)
	}
	start.ActionID = ids[1]
	if r := submit(t, s, start)[0]; !errors.Is(r.Err, ErrCatalogDependency) || r.Allowed {
		t.Fatal("stale queued action started", r.Err)
	}
	// The first action already received a committed start grant. Rotation must
	// not discard its known result merely because the dependency guard is old.
	submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: ids[0], At: now, Outcome: "success"})
	m, _ = s.Get(m.ID)
	if m.Actions[ids[0]].State != Succeeded || m.Actions[ids[1]].State != Queued {
		t.Fatal("result boundary lost committed outcome")
	}
	if r := submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: guard, Generation: 2, At: now, Outcome: "success"})[0]; !errors.Is(r.Err, ErrCatalogDependency) {
		t.Fatal("stale check result could mutate current incident")
	}
}

func TestExecutionGuardRemovalAndRecreation(t *testing.T) {
	s := openCatalogMemory(t)
	m, monitor, _, guard := prepareGuardFixture(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: guard, At: now})
	deleted := catalogSubmit(t, s, deleteCatalogMutation(monitor, "monitor-deleted"))
	if deleted.Err != nil || deleted.Catalog == nil {
		t.Fatal(deleted.Err)
	}
	removed := *deleted.Catalog
	removal := &CatalogGuard{Removed: true, Conditions: []CatalogCondition{{Key: removed.Key, UID: removed.UID, Revision: removed.Revision}}}
	command := Command{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, Guard: removal, At: now}
	if r := submit(t, s, command)[0]; r.Err != nil {
		t.Fatal(r.Err)
	}
	if current, _ := s.Get(m.ID); !current.Removed {
		t.Fatal("committed tombstone not reflected in incident state")
	}
	reborn := catalogRecord(t, s, "Monitor", m.ID, "new-incarnation", "new-version", "same-private-monitor")
	createCatalog(t, s, reborn)
	if err := s.CheckCatalogGuard(m.ID, guard); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("old incarnation guard accepted", err)
	}
	if r := submit(t, s, command)[0]; !errors.Is(r.Err, ErrCatalogDependency) {
		t.Fatal("stale removal could remove recreated monitor", r.Err)
	}
}

func TestExecutionGuardValidationAndLogFormat(t *testing.T) {
	s := openCatalogMemory(t)
	m, _, _, g := prepareGuardFixture(t, s)
	now := time.Now().UTC()
	command := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: now}
	for _, mutate := range []func(*Command){
		func(c *Command) { c.Guard.Conditions = nil },
		func(c *Command) { c.Guard.Conditions = append(c.Guard.Conditions, c.Guard.Conditions[0]) },
		func(c *Command) { c.Guard.Removed = true },
		func(c *Command) { c.Guard.Conditions[0].UID = "" },
		func(c *Command) { c.Kind, c.ActionID = "result", "action" },
	} {
		bad := command
		copy := g.Clone()
		bad.Guard = &copy
		mutate(&bad)
		if _, err := s.Submit(context.Background(), []Command{bad}); err == nil {
			t.Fatal("malformed guard persisted")
		}
	}
	for _, version := range []int{FormatVersion, CatalogFormatVersion} {
		raw, err := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEnvelope(raw)
		if (err == nil) != (version == CatalogFormatVersion) {
			t.Fatal("wrong versioned replay behavior", err)
		}
	}
}

func TestExecutionGuardRecreationCannotAdoptPriorIncarnationActions(t *testing.T) {
	s := openCatalogMemory(t)
	m, monitor, _, guard := prepareGuardFixture(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: guard, At: now})
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: guard, Generation: 1, At: now, Outcome: "failure"})
	old, _ := s.Get(m.ID)
	ids := sortedActions(old.Actions)
	submit(t, s, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, Guard: guard, ActionID: ids[0], At: now})
	if result := catalogSubmit(t, s, deleteCatalogMutation(monitor, "deleted")); result.Err != nil {
		t.Fatal(result.Err)
	}
	newRecord := createCatalog(t, s, catalogRecord(t, s, "Monitor", m.ID, "replacement-uid", "replacement-version", "same configuration"))
	newGuard := &CatalogGuard{Conditions: catalogConditions(t, s, []CatalogKey{newRecord.Key})}
	// The owner missed the intermediate tombstone. Even a fresh catalog guard
	// cannot reinterpret the old executable projection as the new incarnation.
	start := Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, Guard: newGuard, ActionID: ids[1], At: now}
	if result := submit(t, s, start)[0]; !errors.Is(result.Err, ErrCatalogDependency) {
		t.Fatal("fresh catalog guard bypassed projection incarnation", result.Err)
	}
	if err := s.CheckCatalogGuard(m.ID, newGuard); !errors.Is(err, ErrCatalogDependency) {
		t.Fatal("new configuration could run before projection commit", err)
	}
	if result := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: newGuard, At: now})[0]; result.Err != nil {
		t.Fatal(result.Err)
	}
	current, _ := s.Get(m.ID)
	if current.CatalogUID != newRecord.UID || current.Incident || current.Generation != 0 ||
		current.Actions[ids[0]].State != Unknown || current.Actions[ids[1]].State != Cancelled ||
		current.Actions[ids[0]].CatalogUID != monitor.UID {
		t.Fatal("recreation transferred incident ownership or discarded old actions")
	}
	if result := submit(t, s, start)[0]; result.Allowed {
		t.Fatal("new incarnation started an old action")
	}
	submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Guard: newGuard, Generation: 1, At: now, Outcome: "failure"})
	current, _ = s.Get(m.ID)
	newQueued := 0
	for _, action := range current.Actions {
		if action.State == Queued {
			newQueued++
			if action.CatalogUID != newRecord.UID || action.ID == ids[0] || action.ID == ids[1] {
				t.Fatal("new action aliased prior incarnation")
			}
		}
	}
	if newQueued != 2 {
		t.Fatal("new incarnation could not open its own incident")
	}
}

func TestInitialCatalogAdoptionPreservesLegacyIncidentAndActionIDs(t *testing.T) {
	s := openCatalogMemory(t)
	configure(t, s)
	now := time.Now().UTC()
	submit(t, s, Command{Kind: "pulse", MonitorID: "stable-one", Revision: "r1", Generation: 1, At: now, Outcome: "failure"})
	old, _ := s.Get("stable-one")
	m, record, _, guard := prepareGuardFixture(t, s)
	if result := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: guard, At: now})[0]; result.Err != nil {
		t.Fatal(result.Err)
	}
	current, _ := s.Get(m.ID)
	if !current.Incident || current.Generation != old.Generation || len(current.Actions) != len(old.Actions) {
		t.Fatal("initial encrypted migration reset legacy incident")
	}
	for id, before := range old.Actions {
		after := current.Actions[id]
		if before.State != after.State || before.Revision != after.Revision || after.CatalogUID != record.UID {
			t.Fatal("initial encrypted migration regenerated existing action")
		}
	}
}
