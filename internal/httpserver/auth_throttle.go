package httpserver

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	maxAuthSources    = 1024
	maxAuthFailures   = 32
	authFailureWindow = 30 * time.Second
	authFailureHold   = 10 * time.Second
)

type authFailure struct {
	started, blockedUntil time.Time
	failures              int
}

type authThrottle struct {
	mu      sync.Mutex
	sources map[string]authFailure
}

func (t *authThrottle) blocked(source string, at time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Bounded opportunistic expiry; no unbounded per-request map sweep.
	scanned := 0
	for key, failure := range t.sources {
		if scanned == 32 {
			break
		}
		scanned++
		if !at.Before(failure.started.Add(authFailureWindow)) && !at.Before(failure.blockedUntil) {
			delete(t.sources, key)
		}
	}
	failure, found := t.sources[source]
	return at.Before(failure.blockedUntil) || !found && len(t.sources) >= maxAuthSources
}

func (t *authThrottle) failed(source string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sources == nil {
		t.sources = make(map[string]authFailure)
	}
	failure, found := t.sources[source]
	if !found && len(t.sources) >= maxAuthSources {
		return
	}
	if !found || !at.Before(failure.started.Add(authFailureWindow)) {
		failure = authFailure{started: at}
	}
	failure.failures++
	if failure.failures >= maxAuthFailures {
		failure.blockedUntil = at.Add(authFailureHold)
	}
	t.sources[source] = failure
}

type authStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *authStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *authStatusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *authStatusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func failedAuthenticationLimit(next http.Handler) http.Handler {
	throttle := new(authThrottle)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Forwarded addresses are untrusted. Use the connected peer only.
		source, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			source = "unavailable"
		}
		at := time.Now()
		if throttle.blocked(source, at) {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		observed := &authStatusWriter{ResponseWriter: w}
		next.ServeHTTP(observed, r)
		if observed.status == http.StatusUnauthorized {
			throttle.failed(source, time.Now())
		}
	})
}
