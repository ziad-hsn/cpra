package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
)

// CatalogGuard pins the complete resource closure used to prepare executable
// jobs. It contains identities only. Conditions include the Monitor itself and
// every transitive reference; replay checks them without decrypting provider data.
// Removed is used only when the owner removes an already-tombstoned monitor.
type CatalogGuard struct {
	Conditions []CatalogCondition `json:"conditions"`
	Removed    bool               `json:"removed,omitempty"`
}

func (g CatalogGuard) Clone() CatalogGuard {
	g.Conditions = slices.Clone(g.Conditions)
	for i := range g.Conditions {
		if p := g.Conditions[i].ExpectedDependentsVersion; p != nil {
			n := *p
			g.Conditions[i].ExpectedDependentsVersion = &n
		}
	}
	return g
}

func (g CatalogGuard) validate(monitorID string) error {
	if len(g.Conditions) < 1 || len(g.Conditions) > 10000 || (g.Removed && len(g.Conditions) != 1) {
		return errors.New("invalid execution dependency count")
	}
	seen := make(map[CatalogKey]bool, len(g.Conditions))
	root := CatalogKey{Kind: "Monitor", ID: monitorID}
	for _, c := range g.Conditions {
		if c.Key.validate() != nil || !catalogIdentifier(c.UID, 256) || !catalogIdentifier(c.Revision, 256) ||
			c.ExpectedDependentsVersion != nil || seen[c.Key] {
			return errors.New("invalid execution dependency condition")
		}
		seen[c.Key] = true
	}
	if !seen[root] {
		return errors.New("execution guard must identify its monitor")
	}
	return nil
}

// checkCatalogGuard requires the caller to hold f.mu. A failed guard is an
// ordinary configuration conflict, never corruption of the durable store.
func (f *machine) checkCatalogGuard(monitorID string, g *CatalogGuard) error {
	if f.bootstrapPending() {
		return ErrBootstrapPending
	}
	root := CatalogKey{Kind: "Monitor", ID: monitorID}
	_, managed := f.image.Catalog[root.indexKey()]
	if g == nil {
		if managed {
			return ErrCatalogDependency
		}
		return nil // Existing manifest-only owners remain compatible.
	}
	conditions := make(map[CatalogKey]CatalogCondition, len(g.Conditions))
	for _, condition := range g.Conditions {
		conditions[condition.Key] = condition
		current, ok := f.image.Catalog[condition.Key.indexKey()]
		removed := g.Removed && condition.Key == root
		if !ok || current.Removed != removed || current.UID != condition.UID || current.Revision != condition.Revision {
			return ErrCatalogDependency
		}
	}
	// Requiring all edges prevents a caller from checking only the unchanged
	// Monitor while silently omitting a rotated endpoint/credential dependency.
	visited := make(map[CatalogKey]bool, len(conditions))
	queued := map[CatalogKey]bool{root: true}
	queue := []CatalogKey{root}
	for len(queue) != 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if visited[key] {
			continue
		}
		if _, ok := conditions[key]; !ok {
			return ErrCatalogDependency
		}
		visited[key] = true
		for _, ref := range f.image.Catalog[key.indexKey()].References {
			if _, ok := conditions[ref]; !ok {
				return ErrCatalogDependency
			}
			if !queued[ref] {
				queued[ref] = true
				queue = append(queue, ref)
			}
		}
	}
	if len(visited) != len(conditions) {
		return ErrCatalogDependency
	}
	return nil
}

func (g CatalogGuard) monitorUID(id string) string {
	for _, condition := range g.Conditions {
		if condition.Key == (CatalogKey{Kind: "Monitor", ID: id}) {
			return condition.UID
		}
	}
	return ""
}

// CheckCatalogGuard is a bounded read immediately before a health-check worker
// invokes its private job. It opens no storage, jobs or provider connection.
// External actions use a guarded committed "start" command instead: a read is
// not an action start grant and never replaces that durable marker.
func (s *Store) CheckCatalogGuard(monitorID string, g *CatalogGuard) error {
	return s.CheckCatalogGuardContext(context.Background(), monitorID, g)
}

// CheckCatalogGuardContext bounds dependency-read lock acquisition by ctx.
func (s *Store) CheckCatalogGuardContext(ctx context.Context, monitorID string, g *CatalogGuard) error {
	if g != nil {
		if err := g.validate(monitorID); err != nil {
			return err
		}
		if g.Removed {
			return ErrCatalogDependency
		}
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.fsm.checkCatalogGuard(monitorID, g); err != nil {
		return err
	}
	if g != nil && s.fsm.image.Monitors[monitorID].CatalogUID != g.monitorUID(monitorID) {
		return ErrCatalogDependency
	}
	return ctx.Err()
}

// revision identifies the complete non-secret prepared resource closure.
func (g CatalogGuard) revision() string {
	conditions := append([]CatalogCondition(nil), g.Conditions...)
	slices.SortFunc(conditions, func(a, b CatalogCondition) int {
		if a.Key.indexKey() < b.Key.indexKey() {
			return -1
		}
		if a.Key.indexKey() > b.Key.indexKey() {
			return 1
		}
		return 0
	})
	data, _ := json.Marshal(conditions)
	return identity("prepared-dependencies/" + string(data))
}
