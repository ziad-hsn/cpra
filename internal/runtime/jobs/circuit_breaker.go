package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int32

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

// CircuitBreaker implements the circuit breaker pattern for external service calls.
type CircuitBreaker struct {
	state           int32 // CircuitState stored atomically
	failureCount    int64
	lastFailureTime int64 // Unix nano timestamp
	successCount    int64
	maxFailures     int64
	timeout         time.Duration
	halfOpenTimeout time.Duration
}

// CircuitBreakerConfig holds configuration for circuit breaker.
type CircuitBreakerConfig struct {
	MaxFailures     int
	Timeout         time.Duration
	HalfOpenTimeout time.Duration
}

// NewCircuitBreaker creates a new circuit breaker with the given config.
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	if config.MaxFailures <= 0 {
		config.MaxFailures = 5
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.HalfOpenTimeout <= 0 {
		config.HalfOpenTimeout = 30 * time.Second
	}

	return &CircuitBreaker{
		maxFailures:     int64(config.MaxFailures),
		timeout:         config.Timeout,
		halfOpenTimeout: config.HalfOpenTimeout,
	}
}

// Execute wraps a function call with circuit breaker protection.
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	state := CircuitState(atomic.LoadInt32(&cb.state))

	switch state {
	case CircuitOpen:
		if cb.shouldAttemptReset() {
			if atomic.CompareAndSwapInt32(&cb.state, int32(CircuitOpen), int32(CircuitHalfOpen)) {
				return cb.executeInHalfOpenState(ctx, fn)
			}
		}
		return errors.New("circuit breaker is open")
	case CircuitHalfOpen:
		return cb.executeInHalfOpenState(ctx, fn)
	default:
		return cb.executeInClosedState(fn)
	}
}

func (cb *CircuitBreaker) executeInClosedState(fn func() error) error {
	err := fn()
	if err != nil {
		cb.recordFailure()
		if atomic.LoadInt64(&cb.failureCount) >= cb.maxFailures {
			atomic.StoreInt32(&cb.state, int32(CircuitOpen))
		}
		return err
	}
	cb.recordSuccess()
	return nil
}

func (cb *CircuitBreaker) executeInHalfOpenState(ctx context.Context, fn func() error) error {
	err := fn()
	if err != nil {
		cb.recordFailure()
		atomic.StoreInt32(&cb.state, int32(CircuitOpen))
		return err
	}

	cb.recordSuccess()
	atomic.StoreInt32(&cb.state, int32(CircuitClosed))
	atomic.StoreInt64(&cb.failureCount, 0)
	return nil
}

func (cb *CircuitBreaker) recordFailure() {
	atomic.AddInt64(&cb.failureCount, 1)
	atomic.StoreInt64(&cb.lastFailureTime, time.Now().UnixNano())
}

func (cb *CircuitBreaker) recordSuccess() {
	atomic.AddInt64(&cb.successCount, 1)
}

func (cb *CircuitBreaker) shouldAttemptReset() bool {
	lastFailure := atomic.LoadInt64(&cb.lastFailureTime)
	return time.Since(time.Unix(0, lastFailure)) >= cb.timeout
}

// GetState returns the current circuit breaker state.
func (cb *CircuitBreaker) GetState() CircuitState {
	return CircuitState(atomic.LoadInt32(&cb.state))
}

// GetMetrics returns current circuit breaker metrics.
func (cb *CircuitBreaker) GetMetrics() (failures, successes int64, state CircuitState) {
	return atomic.LoadInt64(&cb.failureCount),
		atomic.LoadInt64(&cb.successCount),
		CircuitState(atomic.LoadInt32(&cb.state))
}

// GetDockerCircuitBreaker returns the shared circuit breaker for Docker operations.
func GetDockerCircuitBreaker() *CircuitBreaker {
	dockerCircuitBreakerOnce.Do(func() {
		config := CircuitBreakerConfig{
			MaxFailures:     5,
			Timeout:         60 * time.Second,
			HalfOpenTimeout: 30 * time.Second,
		}
		dockerCircuitBreaker = NewCircuitBreaker(config)
	})
	return dockerCircuitBreaker
}

var (
	dockerCircuitBreaker     *CircuitBreaker
	dockerCircuitBreakerOnce sync.Once
)
