package persistence

import "context"

// GetContext reads an owner projection after checking storage health. Get keeps
// its legacy unchecked read semantics for recovery and diagnostic callers.
func (s *Store) GetContext(ctx context.Context, id string) (Monitor, bool, error) {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return Monitor{}, false, err
	}
	defer unlock()
	m, ok := s.fsm.image.Monitors[id]
	m = m.Clone()
	if err := ctx.Err(); err != nil {
		return Monitor{}, false, err
	}
	return m, ok, nil
}
