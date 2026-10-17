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

func (t *authThrottle) failed(source string, at time.Time) bool {
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
	if t.sources == nil {
		t.sources = make(map[string]authFailure)
	}
	failure, found := t.sources[source]
	if !found && len(t.sources) >= maxAuthSources {
		// Evict the oldest failed source rather than letting a full table deny
		// unrelated callers. The table and this scan have a fixed upper bound.
		var oldest string
		var started time.Time
		for key, entry := range t.sources {
			if oldest == "" || entry.started.Before(started) {
				oldest, started = key, entry.started
			}
		}
		delete(t.sources, oldest)
	}
	if !found || !at.Before(failure.started.Add(authFailureWindow)) {
		failure = authFailure{started: at}
	}
	failure.failures = min(maxAuthFailures, failure.failures+1)
	if failure.failures >= maxAuthFailures {
		failure.blockedUntil = at.Add(authFailureHold)
	}
	t.sources[source] = failure
	return at.Before(failure.blockedUntil)
}

type authStatusWriter struct {
	http.ResponseWriter
	status  int
	fail    func() bool
	limited bool
}

func (w *authStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *authStatusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
		if code == http.StatusUnauthorized && w.fail() {
			w.limited = true
			w.Header().Del("Content-Length")
			w.Header().Del("WWW-Authenticate")
			w.Header().Set("Retry-After", "10")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/problem+json")
			w.ResponseWriter.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.ResponseWriter.Write([]byte(`{"type":"about:blank","title":"Too Many Requests","status":429,"code":"authentication_throttled"}` + "\n"))
			return
		}
	}
	if w.limited {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *authStatusWriter) Write(data []byte) (int, error) {
	if w.limited {
		return len(data), nil
	}
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
		// Authority is checked on every request before failed authentication is
		// throttled. Neither an attacker sharing a peer nor table saturation can
		// turn a valid credential into an authentication failure.
		observed := &authStatusWriter{ResponseWriter: w, fail: func() bool {
			return throttle.failed(source, time.Now())
		}}
		next.ServeHTTP(observed, r)
	})
}
