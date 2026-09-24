package systems

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

var errMonitorPreparation = errors.New("monitor job preparation failed; review driver configuration")

// managedMonitor is owned exclusively by the ECS loop. Workers receive copies
// of its immutable guard; background preparation never reads this map or world.
type managedMonitor struct {
	guard        persistence.CatalogGuard
	uid, version string
}

type catalogProjection struct {
	control  *persistence.OperationReceipt
	prepared *entities.PreparedMonitor
	runtime  management.RuntimePreparation
	removed  *persistence.CatalogRecord
	done     chan error
}

// catalogRuntime prepares one configuration at a time outside the ECS owner.
// Its single-slot handoff bounds retained decrypted configuration. known holds
// identities only, for removal reconciliation after an explicit change-stream gap.
type catalogRuntime struct {
	catalog   *management.Catalog
	store     *persistence.Store
	mapper    *entities.EntityManager
	cursor    persistence.CatalogCursor
	known     map[string]bool
	updates   chan catalogProjection
	cancel    context.CancelFunc
	ctx       context.Context
	done      chan struct{}
	startOnce sync.Once
}

func (r *catalogRuntime) start() { r.startOnce.Do(func() { go r.run() }) }

// LoadCatalog runs before workers or the owner start. Provider jobs are built
// privately and the complete catalog has already been authenticated by startup.
func (s *DurableSystem) LoadCatalog(ctx context.Context, catalog *management.Catalog, mapper *entities.EntityManager) error {
	if catalog == nil || mapper == nil || len(s.entities) != 0 {
		return errors.New("catalog loading requires an empty owner and verified catalog")
	}
	view, err := catalog.SnapshotContext(ctx)
	if err != nil {
		return err
	}
	r := &catalogRuntime{catalog: catalog, store: s.store, mapper: mapper, cursor: view.ChangeCursor(), known: map[string]bool{}, updates: make(chan catalogProjection, 1), done: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	s.catalogRuntime = r
	s.managed = make(map[string]managedMonitor)
	if err := s.loadCatalogBatches(ctx, view); err != nil {
		r.cancel()
		return err
	}
	// The old manifest may have removed monitors before adoption. Reconcile
	// absent durable records while no worker can execute their queued actions.
	return s.store.Reconcile(ctx, r.known, time.Now().UTC())
}

func (r *catalogRuntime) prepare(ctx context.Context, view management.ReadView, id string) (catalogProjection, error) {
	p, err := r.catalog.PrepareRuntime(ctx, view, id)
	if err != nil {
		return catalogProjection{}, err
	}
	prepared, err := entities.PrepareMonitor(p.Monitor, p.Endpoints, p.NotificationGroups)
	if err != nil {
		return catalogProjection{}, errMonitorPreparation
	}
	return catalogProjection{prepared: prepared, runtime: p}, nil
}

func (r *catalogRuntime) scan(ctx context.Context, view management.ReadView, apply func(catalogProjection) error) error {
	for after := ""; ; {
		rows, next, err := view.Page(ctx, "Monitor", after, 100)
		if err != nil {
			return err
		}
		for _, row := range rows {
			p, err := r.prepare(ctx, view, row.Metadata.ID)
			if err != nil {
				return err
			}
			err = apply(p)
			if err != nil {
				return err
			}
			r.known[row.Metadata.ID] = true
		}
		if next == "" {
			break
		}
		after = next
	}
	// This O(N) reconciliation occurs only at startup or a reported ring gap,
	// outside the owner. Normal changes follow the reverse dependency index.
	for id := range r.known {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, ok, err := r.store.CatalogRetainedContext(ctx, persistence.CatalogKey{Kind: "Monitor", ID: id})
		if err != nil {
			return err
		}
		if ok && record.Removed {
			if err := apply(catalogProjection{removed: &record}); err != nil {
				return err
			}
			delete(r.known, id)
		}
	}
	return nil
}

func (r *catalogRuntime) handoff(p catalogProjection) error {
	p.done = make(chan error, 1)
	select {
	case r.updates <- p:
	case <-r.ctx.Done():
		if p.prepared != nil {
			_ = p.prepared.Close()
		}
		return r.ctx.Err()
	}
	// Ownership transfers with the send. Cancellation must not clear a bundle
	// while the owner is installing it; the owner/finalizer closes it exactly once.
	select {
	case err := <-p.done:
		return err
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func (r *catalogRuntime) run() {
	defer close(r.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var receiptsAt time.Time
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		changes, err := r.store.CatalogChangesSinceContext(r.ctx, r.cursor, 100)
		if errors.Is(err, persistence.ErrCatalogChangeGap) {
			view, e := r.catalog.SnapshotContext(r.ctx)
			if e == nil {
				e = r.scan(r.ctx, view, r.handoff)
			}
			if e == nil {
				r.cursor = view.ChangeCursor()
			}
			continue
		}
		if err != nil {
			continue
		}
		for _, change := range changes.Changes {
			// Independent resources must not wait behind one failed preparation.
			// The retained receipt retries transient conflicts on the next pass.
			_ = r.reconcileKey(change.Key)
		}
		r.cursor = changes.Next
		if time.Since(receiptsAt) < 5*time.Second {
			continue
		}
		receiptsAt = time.Now()
		r.completeReceipts()
	}
}

// reconcileKey traverses at most the same 10,000-node graph admitted by a
// synchronous mutation. A mutation exceeding it requires collection processing;
// an unexpectedly larger graph remains pending rather than falsely applied.
func (r *catalogRuntime) reconcileKey(key persistence.CatalogKey) error {
	queue := []persistence.CatalogKey{key}
	seen := map[persistence.CatalogKey]bool{key: true}
	for len(queue) > 0 {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		key, queue = queue[0], queue[1:]
		record, ok, err := r.store.CatalogRetainedContext(r.ctx, key)
		if err != nil {
			return err
		}
		if !ok {
			return persistence.ErrCatalogNotFound
		}
		if key.Kind == "Monitor" {
			if record.Removed {
				if err := r.handoff(catalogProjection{removed: &record}); err != nil {
					return err
				}
				delete(r.known, key.ID)
			} else {
				view, err := r.catalog.SnapshotContext(r.ctx)
				if err != nil {
					return err
				}
				p, err := r.prepare(r.ctx, view, key.ID)
				if err != nil {
					return err
				}
				if err := r.handoff(p); err != nil {
					return err
				}
				r.known[key.ID] = true
			}
			continue
		}
		if record.Removed {
			continue
		} // deletion was admitted only without references
		dependents, _, err := r.store.CatalogDependentsContext(r.ctx, key, 10000)
		if err != nil {
			return err
		}
		for _, dependent := range dependents {
			if !seen[dependent] {
				if len(seen) >= 10000 {
					return management.ErrGraphLimit
				}
				seen[dependent] = true
				queue = append(queue, dependent)
			}
		}
	}
	return nil
}

func (r *catalogRuntime) completeReceipts() {
	receipts, err := r.store.PendingOperationsContext(r.ctx)
	if err != nil {
		return
	}
	for _, receipt := range receipts {
		if err := r.ctx.Err(); err != nil {
			return
		}
		if receipt.Subject != "" {
			if err := r.handoff(catalogProjection{control: &receipt}); err == nil {
				_ = r.catalog.CompleteOperation(r.ctx, receipt.ID, true)
			}
			continue
		}
		// Reconcile from a fresh view, then let the durable completion CAS check
		// the receipt's exact version. A superseded write cannot claim success.
		if err := r.reconcileKey(receipt.Key); err != nil {
			if errors.Is(err, errMonitorPreparation) {
				_ = r.catalog.CompleteOperation(r.ctx, receipt.ID, false)
			}
			continue
		}
		_ = r.catalog.CompleteOperation(r.ctx, receipt.ID, true)
	}
}

func (s *DurableSystem) receiveCatalogProjection() {
	if s.catalogRuntime == nil || s.stopping.Load() {
		return
	}
	select {
	case p := <-s.catalogRuntime.updates:
		s.startCatalogProjection(p)
	default:
	}
}

func (s *DurableSystem) startCatalogProjection(update catalogProjection) {
	s.beginOwnerWrite(&ownerWrite{kind: catalogWrite, projection: &update})
}

func (s *DurableSystem) prepareCatalogWrite(ctx context.Context, p *ownerWrite) error {
	update := *p.projection
	p.confirmed = true
	if update.removed != nil {
		record := update.removed
		guard := persistence.CatalogGuard{Removed: true, Conditions: []persistence.CatalogCondition{{Key: record.Key, UID: record.UID, Revision: record.Revision}}}
		m, ok, err := s.store.GetContext(ctx, record.Key.ID)
		if err != nil {
			return err
		}
		if ok && !m.Removed {
			p.commands = []persistence.Command{{Kind: "remove", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), Guard: &guard}}
			p.confirmed = false
		}
	} else if update.prepared != nil {
		previous, exists := s.managed[update.prepared.MonitorID()]
		p.installed = exists && previous.version == update.runtime.ProjectionVersion
		if !p.installed {
			p.commands = []persistence.Command{s.catalogConfigureCommand(update)}
			p.confirmed = false
		}
	}
	return nil
}

func (s *DurableSystem) finishCatalogWrite(ctx context.Context, p *ownerWrite) error {
	update := *p.projection
	var err error
	if len(p.results) != 0 {
		if p.results[0].Err != nil && p.results[0].Monitor != nil {
			return persistence.ErrCommitUnconfirmed
		}
		err = p.results[0].Err
	}
	if err == nil {
		switch {
		case update.control != nil:
			err = s.applyControlReceipt(ctx, *update.control)
		case update.removed != nil:
			err = s.removeCatalogProjection(ctx, *update.removed)
		case update.prepared != nil:
			guard := persistence.CatalogGuard{Conditions: slices.Clone(update.runtime.Conditions)}
			id := update.prepared.MonitorID()
			// Never install a stale preparation after a newer catalog mutation.
			err = s.store.CheckCatalogGuardContext(ctx, id, &guard)
			if err == nil && !p.installed {
				if len(p.results) != 1 || p.results[0].Monitor == nil {
					return management.ErrOutcomeUnconfirmed
				}
				m := p.results[0].Monitor
				if m.ID != id || m.CatalogUID != update.runtime.MonitorUID || m.CatalogRevision != update.runtime.ResourceVersion || m.Revision != update.runtime.ExecutionRevision || m.Removed {
					return persistence.ErrCommitUnconfirmed
				}
				p.installed = true
				err = s.installCatalogProjection(ctx, update, *m)
			} else if err == nil {
				err = s.store.MarkMonitorObservedContext(ctx, id, guard, update.runtime.Generation)
			}
		default:
			return errors.New("empty catalog owner projection")
		}
	}
	if persistence.IsLeadershipUnavailable(err) || ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err // Keep the preparation and acknowledgement owned here.
	}
	if update.prepared != nil {
		_ = update.prepared.Close()
	}
	if update.done != nil {
		update.done <- err
	}
	return nil
}

func (s *DurableSystem) catalogConfigureCommand(update catalogProjection) persistence.Command {
	p := update.runtime
	id := update.prepared.MonitorID()
	guard := persistence.CatalogGuard{Conditions: slices.Clone(p.Conditions)}
	policy := persistence.Policy{Interval: p.Monitor.Pulse.Interval, Unhealthy: p.Monitor.Pulse.UnhealthyThreshold, Healthy: max(1, p.Monitor.Pulse.HealthyThreshold), Intervention: p.Monitor.Intervention.Action != "", Enabled: p.Monitor.Enabled, Cooldown: s.cooldown, RecoveryBypass: s.recoveryBypass, Endpoints: map[string]int{}}
	if p.RecoveryCooldown != nil {
		policy.RecoveryCooldown = *p.RecoveryCooldown
	}
	if p.RecoveryMaxAttempts != nil {
		policy.RecoveryMaxAttempts = *p.RecoveryMaxAttempts
	}
	for _, w := range p.AbsoluteMaintenance {
		policy.Maintenance = append(policy.Maintenance, persistence.MaintenanceWindow{Start: w.Start, End: w.End})
	}
	for color, c := range p.Monitor.Codes {
		if c.Dispatch {
			policy.Endpoints[color] = len(p.NotificationTargets[color])
		}
	}
	m := persistence.Monitor{ID: id, Name: p.Monitor.Name, Revision: p.ExecutionRevision, CatalogUID: p.MonitorUID, Policy: policy}
	return persistence.Command{Kind: "configure", MonitorID: id, Revision: m.Revision, Config: &m, Guard: &guard, At: time.Now().UTC()}
}

func (s *DurableSystem) installCatalogProjection(ctx context.Context, update catalogProjection, current persistence.Monitor) error {
	p := update.runtime
	id := update.prepared.MonitorID()
	previous := s.managed[id]
	policy := current.Policy
	guard := persistence.CatalogGuard{Conditions: slices.Clone(p.Conditions)}
	var err error
	// The configure command is the ordering point. Later concurrent catalog
	// changes are fenced by every dispatch and result guard until reprojected.
	ent, found := s.entities[id]
	wasDisabled := found && s.disabled.Get(ent) != nil
	if found {
		s.cancelSchedules(ent, id)
	}
	if found && previous.uid != p.MonitorUID {
		s.removePause(ent, time.Now())
		if err = s.catalogRuntime.mapper.Remove(ent, s.world); err != nil {
			s.fail(err)
			return err
		}
		if s.index != nil {
			s.index.Remove(ent.ID())
		}
		delete(s.entities, id)
		found = false
	}
	if found {
		err = s.catalogRuntime.mapper.Replace(ent, update.prepared, s.world)
	} else {
		ent, err = s.catalogRuntime.mapper.Install(update.prepared, s.world)
	}
	if err != nil {
		s.fail(errors.New("committed monitor could not be installed"))
		return err
	}
	s.entities[id] = ent
	s.managed[id] = managedMonitor{guard: guard, uid: p.MonitorUID, version: p.ProjectionVersion}
	s.states.Get(ent).Revision = p.ExecutionRevision
	// Desired disablement must update the ECS exclusion tag after replacement;
	// the manager preserves it so future independent controls are not lost.
	if !policy.Enabled && s.disabled.Get(ent) == nil {
		s.disabled.Add(ent, &components.Disabled{})
	}
	if policy.Enabled && s.disabled.Get(ent) != nil {
		s.disabled.Remove(ent)
	}
	for int(ent.ID()) >= len(s.checks) {
		s.checks = append(s.checks, checkSchedule{})
	}
	if policy.Enabled && current.SnoozedUntil.IsZero() {
		due := current.NextCheck
		if current.Generation == 0 || wasDisabled {
			due = time.Now().Add(staggerPhase(ent.ID(), policy.Interval))
		}
		s.checks[ent.ID()].next = due.UnixNano()
		s.scheduler.Schedule(ent, due)
	}
	s.project(ent, current)
	s.scheduleActions(ent, current)
	if s.index != nil {
		s.index.Put(s.summary(ent, current))
		// Startup's Initialize still needs to build the complete index and
		// schedules. It publishes its acknowledgements only after that work.
		return s.store.MarkMonitorObservedContext(ctx, id, guard, p.Generation)
	}
	return nil
}

// observeLoadedCatalog runs after all startup projections and the shared index
// are installed. It cannot acknowledge a concurrently changed configuration.
func (s *DurableSystem) observeLoadedCatalog() error {
	for id, installed := range s.managed {
		record, ok, err := s.store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: id})
		if err != nil {
			return err
		}
		if !ok {
			continue // A removal is reconciled by the guarded background handoff.
		}
		err = s.store.MarkMonitorObserved(id, installed.guard, record.Generation)
		if err != nil && !errors.Is(err, persistence.ErrCatalogConflict) && !errors.Is(err, persistence.ErrCatalogDependency) {
			return err
		}
	}
	return nil
}

func (s *DurableSystem) cancelSchedules(ent ecs.Entity, id string) {
	s.scheduler.Cancel(ent)
	s.actions.Cancel(ent)
	s.snoozes.Cancel(ent)
	delete(s.actionDue, ent)
	delete(s.pending, id)
	delete(s.pendingCheckID, id)
	if int(ent.ID()) < len(s.checks) {
		s.resetCheckSchedule(ent)
	}
}

func (s *DurableSystem) removeCatalogProjection(ctx context.Context, record persistence.CatalogRecord) error {
	current, ok, err := s.store.CatalogRetainedContext(ctx, record.Key)
	if err != nil {
		return err
	}
	if !ok || !current.Removed || current.UID != record.UID || current.Revision != record.Revision {
		return persistence.ErrCatalogDependency
	}
	if ent, exists := s.entities[record.Key.ID]; exists {
		s.cancelSchedules(ent, record.Key.ID)
		s.removePause(ent, time.Now())
		if err := s.catalogRuntime.mapper.Remove(ent, s.world); err != nil {
			return fmt.Errorf("remove operational monitor: %w", err)
		}
		if s.index != nil {
			s.index.Remove(ent.ID())
		}
		delete(s.entities, record.Key.ID)
		delete(s.managed, record.Key.ID)
	}
	return nil
}
