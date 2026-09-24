package persistence

import (
	"context"
	"errors"
)

var errCatalogUnavailable = errors.New("durable catalog unavailable")

// Native catalog reads support startup verification and administrative recovery
// before runtime admission opens. The management layer checks operational health
// separately; these reads reject recorded storage failure and partial bootstrap.
func (s *Store) lockCatalogReadState(ctx context.Context, write bool) (func(), error) {
	unlock, err := s.lockStateContext(ctx, write)
	if err != nil {
		return nil, err
	}
	if s.err != nil || s.fsm.err != nil {
		unlock()
		return nil, errCatalogUnavailable
	}
	if s.fsm.bootstrapPending() {
		unlock()
		return nil, ErrBootstrapPending
	}
	return unlock, nil
}
