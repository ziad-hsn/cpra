package jobs

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

// EffectOutcome describes what the executor can prove, independently of retry
// policy. An ordinary error carries no proof that the remote effect was absent.
type EffectOutcome uint8

const (
	EffectUnknown EffectOutcome = iota
	EffectSucceeded
	EffectNotApplied
)

type EffectResult struct {
	Outcome    EffectOutcome
	Retryable  bool
	RetryAfter time.Duration
}

// ClassifyEffect preserves provider rejection evidence through error wrapping.
// Only the driver, at a known protocol boundary, may assert no effect.
func ClassifyEffect(err error) EffectResult {
	if err == nil {
		return EffectResult{Outcome: EffectSucceeded}
	}
	var local *noEffectError
	if errors.As(err, &local) {
		return EffectResult{Outcome: EffectNotApplied, Retryable: local.retryable}
	}
	var rejection *DeliveryError
	if errors.As(err, &rejection) {
		return EffectResult{Outcome: EffectNotApplied, Retryable: rejection.Retryable, RetryAfter: rejection.RetryAfter}
	}
	if errors.Is(err, ErrInterventionRejected) {
		return EffectResult{Outcome: EffectNotApplied}
	}
	return EffectResult{Outcome: EffectUnknown}
}

type noEffectError struct {
	cause     error
	retryable bool
}

func (e *noEffectError) Error() string { return e.cause.Error() }
func (e *noEffectError) Unwrap() error { return e.cause }

func noEffect(err error, retryable bool) error {
	if err == nil {
		return nil
	}
	return &noEffectError{cause: err, retryable: retryable}
}

// Called only before application transmission, never on an arbitrary failed
// operation. A refused follow-up read cannot classify an earlier mutation.
func transientBeforeEffect(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && (network.Timeout() || network.Temporary())
}

// effectRecorder retains positive transport evidence for a single SDK operation
// whose error decoration may discard wrapped network errors. A possible earlier
// mutation prevents a failed subsequent request from proving no effect.
type effectRecorder struct {
	effectTransport
	mu       sync.Mutex
	last     EffectResult
	possible bool
}

func (r *effectRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := r.effectTransport.RoundTrip(request)
	result := ClassifyEffect(err)
	r.mu.Lock()
	r.last = result
	if request.Method != http.MethodGet && request.Method != http.MethodHead && result.Outcome != EffectNotApplied {
		r.possible = true
	}
	r.mu.Unlock()
	return response, err
}

func (r *effectRecorder) restore(err error) error {
	if err == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.possible && r.last.Outcome == EffectNotApplied {
		return noEffect(err, r.last.Retryable)
	}
	return err
}
