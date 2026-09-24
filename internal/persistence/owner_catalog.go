package persistence

import "context"

// CatalogRetained is the owner-reconciliation lookup, including committed
// deletion tombstones. Public active-resource reads use CatalogGet instead.
// Tombstones are needed to fence removal against an exact incarnation/version.
func (s *Store) CatalogRetained(key CatalogKey) (CatalogRecord, bool, error) {
	return s.CatalogRetainedContext(context.Background(), key)
}

// CatalogRetainedContext reads a retained record within the caller's deadline.
func (s *Store) CatalogRetainedContext(ctx context.Context, key CatalogKey) (CatalogRecord, bool, error) {
	if err := key.validate(); err != nil {
		return CatalogRecord{}, false, err
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return CatalogRecord{}, false, err
	}
	defer unlock()
	r, ok := s.fsm.image.Catalog[key.indexKey()]
	r = r.Clone()
	if err := ctx.Err(); err != nil {
		return CatalogRecord{}, false, err
	}
	return r, ok, nil
}
