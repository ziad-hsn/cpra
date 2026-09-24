package persistence

import (
	"context"
	"math"
)

// ownerObservation is an in-process acknowledgement of installed runtime state.
// It is unexported and never serialized in a Raft command, snapshot or backup.
// installMonitor clears it whenever the identity tuple on Monitor changes.
type ownerObservation struct {
	generation      uint64
	catalogSequence uint64
}

// MarkMonitorObserved is called by the sole controller owner only after it has
// installed the exact jobs, ECS components and schedules described by guard.
// A committed configure command alone must never call this method. It records
// no durable command and grants no permission to execute an external action.
func (s *Store) MarkMonitorObserved(id string, guard CatalogGuard, generation uint64) error {
	return s.MarkMonitorObservedContext(context.Background(), id, guard, generation)
}

// MarkMonitorObservedContext bounds the owner acknowledgement by ctx.
func (s *Store) MarkMonitorObservedContext(ctx context.Context, id string, guard CatalogGuard, generation uint64) error {
	if guard.Removed || guard.validate(id) != nil || generation == 0 || generation > math.MaxInt64 {
		return ErrCatalogConflict
	}
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return err
	}
	defer unlock()
	f := s.fsm
	if err := f.checkCatalogGuard(id, &guard); err != nil {
		return err
	}
	m, ok := f.image.Monitors[id]
	root, exists := f.image.Catalog[(CatalogKey{Kind: "Monitor", ID: id}).indexKey()]
	if !ok || !exists || m.Removed || root.Removed || m.CatalogUID != root.UID || m.CatalogRevision != root.Revision || root.Generation != generation || m.DependencyRevision != guard.revision() {
		return ErrCatalogDependency
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.ownerObserved = ownerObservation{generation: generation, catalogSequence: f.catalogSequence}
	f.image.Monitors[id] = m
	return nil
}

// refreshOwnerObservation runs under f.mu only after a catalog mutation. It
// revalidates this one bounded identity closure, never the fleet or provider
// configuration. The cached sequence keeps ordinary status reads O(1).
func (f *machine) refreshOwnerObservation(m Monitor) Monitor {
	m, _ = f.refreshOwnerObservationContext(context.Background(), m)
	return m
}

func (f *machine) refreshOwnerObservationContext(ctx context.Context, m Monitor) (Monitor, error) {
	if m.ownerObserved.generation == 0 || m.ownerObserved.catalogSequence == f.catalogSequence {
		return m, ctx.Err()
	}
	rootKey := CatalogKey{Kind: "Monitor", ID: m.ID}
	root, ok := f.image.Catalog[rootKey.indexKey()]
	valid := ok && !root.Removed && !m.Removed && root.UID == m.CatalogUID && root.Revision == m.CatalogRevision && root.Generation == m.ownerObserved.generation
	if valid {
		guard := CatalogGuard{}
		queue := []CatalogKey{rootKey}
		seen := map[CatalogKey]bool{rootKey: true}
		for len(queue) > 0 && valid {
			if err := ctx.Err(); err != nil {
				return Monitor{}, err
			}
			key := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			record, exists := f.image.Catalog[key.indexKey()]
			if !exists || record.Removed {
				valid = false
				break
			}
			guard.Conditions = append(guard.Conditions, CatalogCondition{Key: key, UID: record.UID, Revision: record.Revision})
			for _, ref := range record.References {
				if !seen[ref] {
					if len(seen) >= 10000 {
						valid = false
						break
					}
					seen[ref] = true
					queue = append(queue, ref)
				}
			}
		}
		valid = valid && guard.revision() == m.DependencyRevision
	}
	if err := ctx.Err(); err != nil {
		return Monitor{}, err
	}
	if valid {
		m.ownerObserved.catalogSequence = f.catalogSequence
	} else {
		m.ownerObserved = ownerObservation{}
	}
	f.image.Monitors[m.ID] = m
	return m, nil
}

func preserveOwnerObservation(previous Monitor, m *Monitor) {
	// Marker identity is the surrounding monitor's exact UID, catalog revision,
	// dependency closure and executable revision; no duplicate strings/guard are
	// retained for each of a million monitors. Caller-supplied markers are ignored.
	m.ownerObserved = previous.ownerObserved
	if m.Removed || previous.ID != m.ID || previous.CatalogUID != m.CatalogUID || previous.CatalogRevision != m.CatalogRevision || previous.DependencyRevision != m.DependencyRevision || previous.Revision != m.Revision {
		m.ownerObserved = ownerObservation{}
	}
}
