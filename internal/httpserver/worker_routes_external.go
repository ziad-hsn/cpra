//go:build externaljobs

package httpserver

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// The request permits five seconds for authentication/body decoding, a 25-second
// poll, five seconds for commit, and response transmission before the socket ends.
const workerPollWriteTimeout = 40 * time.Second

type serverExtensions struct{ workerHTTP *workerSessionHTTP }

func (c ServerConfig) defaultWriteTimeout() time.Duration {
	if c.ExternalJobsEnabled {
		return workerPollWriteTimeout
	}
	return 30 * time.Second
}

func (s *Server) registerWorkerRoutes(mux *http.ServeMux) {
	if s.workerHTTP == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h := &workerSessionHTTP{
			auth: s.cfg.ManagementAuth, store: s.cfg.Store, catalog: s.cfg.Management,
			admissionContext: ctx, stopAdmission: cancel,
			ready: func(ctx context.Context) error {
				if !s.mutationReady(ctx) {
					if err := ctx.Err(); err != nil {
						return err
					}
					return persistence.ErrWorkerSessionUnavailable
				}
				return s.cfg.Store.ControllerHealthContext(ctx)
			},
			admit: s.withMutationAdmission, completionReady: s.cfg.Store.ControllerHealthContext,
		}
		// The enclosing route owns the entire request, including authentication
		// and encrypted preparation before this commit callback is reached.
		h.complete = func(ctx context.Context, commit func() error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return commit()
		}
		s.workerHTTP = h
		if s.admissionStopped() {
			cancel()
		}
	}
	// Worker handlers authorize against committed worker credentials. Operator
	// management permissions cannot confer execution authority.
	h := s.workerHTTP
	for _, route := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"poll", h.poll}, {"start", h.start}, {"result", h.result},
		{"heartbeat", h.heartbeat}, {"late-evidence", h.evidence},
	} {
		mux.HandleFunc("POST /api/v2/external-workers/"+route.path, func(w http.ResponseWriter, r *http.Request) {
			err := h.completions.admit(r.Context(), func() error {
				route.handler(w, r)
				return nil
			})
			if err != nil {
				managementHeaders(w)
				writeWorkerSessionError(w, err)
			}
		})
	}
}

func (s *Server) stopExtensionAdmission() {
	if s.workerHTTP != nil {
		s.workerHTTP.stopAdmission()
	}
}

func (s *Server) stopExtensions(ctx context.Context) error {
	if s.workerHTTP == nil {
		return nil
	}
	return s.workerHTTP.completions.stop(ctx)
}

// Counts complete worker HTTP requests, never a worker's remote execution.
// StopContext fences further requests and joins this barrier even when closing
// a connection did not finish authentication, preparation or result commit.
type workerCompletionDrain struct {
	mu      sync.Mutex
	stopped bool
	active  int
	done    chan struct{}
}

func (d *workerCompletionDrain) admit(ctx context.Context, commit func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return persistence.ErrWorkerSessionUnavailable
	}
	d.active++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.active--
		if d.stopped && d.active == 0 {
			close(d.done)
		}
	}()
	return commit()
}

func (d *workerCompletionDrain) stop(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	d.mu.Lock()
	if !d.stopped {
		d.stopped, d.done = true, make(chan struct{})
		if d.active == 0 {
			close(d.done)
		}
	}
	done := d.done
	d.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
