package httpserver

import (
	"context"
	"net/http"
)

// StopAdmission first rejects new management commits and marks API readiness
// unavailable, then joins admission callbacks already running. Reads and diagnostics stay
// available. The application must finish this barrier before draining the
// controller, flush the durable command queue to resolve pending Raft admission,
// and preserve store/key ownership if either deadline expires. A callback's
// uncertain result is not proof that its Raft command finished applying.
// Repeated calls wait on the same barrier; cancellation never reopens admission.
func (s *Server) StopAdmission(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	s.mutationMu.Lock()
	if !s.mutationStopped {
		s.mutationStopped = true
		s.mutationsDrained = make(chan struct{})
		if s.activeMutations == 0 {
			close(s.mutationsDrained)
		}
	}
	done := s.mutationsDrained
	s.mutationMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) admissionStopped() bool {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.mutationStopped
}

func mutationUnavailable() error {
	return managementFailure(http.StatusServiceUnavailable, "admissionUnavailable", "Management is not accepting writes. This request was not admitted.")
}

func (s *Server) mutationReady() bool {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return !s.mutationStopped && (s.cfg.Ready == nil || s.cfg.Ready())
}

// withMutationAdmission orders the commit against StopAdmission without holding
// a mutex during encryption, storage or a shutdown wait. The callback finishes
// at the durable admission/result boundary, never after controller/provider work.
func (s *Server) withMutationAdmission(ctx context.Context, commit func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mutationMu.Lock()
	if s.mutationStopped || s.cfg.Ready != nil && !s.cfg.Ready() {
		s.mutationMu.Unlock()
		return mutationUnavailable()
	}
	s.activeMutations++
	s.mutationMu.Unlock()
	defer func() {
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		s.activeMutations--
		if s.mutationStopped && s.activeMutations == 0 {
			close(s.mutationsDrained)
		}
	}()
	return commit()
}
