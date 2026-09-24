//go:build externaljobs

package httpauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

var (
	ErrWorkerAuthentication = errors.New("worker authentication required")
	ErrWorkerForbidden      = errors.New("worker operation forbidden")
	ErrWorkerUnavailable    = errors.New("worker authentication unavailable")
)

// WorkerError exposes a stable HTTP classification without backend diagnostics.
type WorkerError struct{ status int }

func (e *WorkerError) Error() string { return e.Unwrap().Error() }
func (e *WorkerError) Unwrap() error {
	switch e.status {
	case http.StatusUnauthorized:
		return ErrWorkerAuthentication
	case http.StatusForbidden:
		return ErrWorkerForbidden
	default:
		return ErrWorkerUnavailable
	}
}
func (e *WorkerError) StatusCode() int {
	if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
		return e.status
	}
	return http.StatusServiceUnavailable
}

// WorkerPolicyReader authenticates against current committed worker credentials.
type WorkerPolicyReader interface {
	AuthenticateWorker(context.Context, string, time.Time) (persistence.WorkerAuthority, error)
}

// AuthorizeWorker validates a protocol request without reading its body. The
// returned identity grants no execution permission; Start must check committed
// ownership, policy, target incarnation, controls and deadlines in the FSM.
func (a *Authorizer) AuthorizeWorker(r *http.Request, operationID string, source WorkerPolicyReader) (persistence.WorkerAuthority, error) {
	var empty persistence.WorkerAuthority
	if a == nil || source == nil {
		return empty, &WorkerError{status: http.StatusServiceUnavailable}
	}
	if r == nil || r.URL == nil {
		return empty, &WorkerError{status: http.StatusForbidden}
	}
	if err := r.Context().Err(); err != nil {
		return empty, err
	}
	a.mu.RLock()
	verifier, at, err := workerRequest(a.current, r, operationID, a.now())
	a.mu.RUnlock()
	if err != nil {
		return empty, err
	}
	// No policy lock is held while waiting for the committed state reader.
	authority, err := source.AuthenticateWorker(r.Context(), verifier, at)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return empty, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return empty, context.DeadlineExceeded
		case errors.Is(err, persistence.ErrWorkerUnauthorized):
			return empty, &WorkerError{status: http.StatusUnauthorized}
		default:
			return empty, &WorkerError{status: http.StatusServiceUnavailable}
		}
	}
	if err := r.Context().Err(); err != nil {
		return empty, err
	}
	return authority, nil
}

func workerRequest(p *policy, r *http.Request, operationID string, at time.Time) (string, time.Time, error) {
	deny := func(status int) (string, time.Time, error) {
		return "", time.Time{}, &WorkerError{status: status}
	}
	if p == nil {
		return deny(http.StatusServiceUnavailable)
	}
	var path string
	switch operationID {
	case "WorkerPoll":
		path = "/api/v2/external-workers/poll"
	case "WorkerStart":
		path = "/api/v2/external-workers/start"
	case "WorkerHeartbeat":
		path = "/api/v2/external-workers/heartbeat"
	case "WorkerResult":
		path = "/api/v2/external-workers/result"
	case "WorkerLateEvidence":
		path = "/api/v2/external-workers/late-evidence"
	default:
		return deny(http.StatusForbidden)
	}
	if r.Method != http.MethodPost || r.URL.Path != path || r.URL.EscapedPath() != path ||
		r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || r.URL.User != nil {
		return deny(http.StatusForbidden)
	}
	origin, ok := secureOrigin(p, r)
	if !ok || !validOrigin(r, origin) || !validContentType(r, operation{
		bodyRequired: true, mediaTypes: map[string]bool{"application/json": true},
	}) {
		return deny(http.StatusForbidden)
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > MaxAuthorizationBytes {
		return deny(http.StatusUnauthorized)
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) < 32 || !validToken(token) {
		return deny(http.StatusUnauthorized)
	}
	digest := sha256.Sum256([]byte(token))
	managementCredential := 0
	for _, entry := range p.principals {
		managementCredential |= subtle.ConstantTimeCompare(digest[:], entry.digest[:])
	}
	if p.legacy != nil {
		managementCredential |= subtle.ConstantTimeCompare(digest[:], p.legacy[:])
	}
	if managementCredential != 0 {
		return deny(http.StatusUnauthorized)
	}
	return hex.EncodeToString(digest[:]), at, nil
}
