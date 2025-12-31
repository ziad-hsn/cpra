// Package jobs provides concurrency helper patterns from "Concurrency in Go" by Katherine Cox-Buday.
// These utilities enable clean, context-aware concurrent operations.
package jobs

import (
	"context"
	"time"
)

// RetryWithBackoff executes fn up to `attempts` times with exponential backoff.
// Respects context cancellation between attempts.
//
// Book reference: p. 5-6 - "Introducing sleeps into your code can be a handy way
// to debug concurrent programs, but they are not a solution."
//
// Unlike time.Sleep, this function:
//   - Checks context cancellation before each attempt
//   - Uses select with time.After for interruptible waiting
//   - Implements exponential backoff capped at maxDelay
//
// Example:
//
//	err := RetryWithBackoff(ctx, 3, 50*time.Millisecond, func() error {
//	    return doNetworkCall()
//	})
func RetryWithBackoff(ctx context.Context, attempts int, baseDelay time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}

	const maxDelay = time.Second // Cap backoff at 1 second

	var lastErr error
	for i := 0; i < attempts; i++ {
		// Check context before attempting
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		// Don't wait after last attempt
		if i < attempts-1 {
			// Exponential backoff: 50ms, 100ms, 200ms, 400ms, 800ms, 1000ms (capped)
			delay := baseDelay * time.Duration(1<<uint(i))
			if delay > maxDelay {
				delay = maxDelay
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				// Continue to next attempt
			}
		}
	}
	return lastErr
}

// Or combines multiple done channels into a single channel that closes
// when ANY of the input channels closes.
//
// Book reference: p. 94 - The or-channel pattern.
//
// This is useful for combining multiple cancellation signals:
//
//	done := Or(ctx.Done(), stopCh, timeoutCh)
//	select {
//	case <-done:
//	    // One of the signals fired
//	case result := <-resultCh:
//	    // Got result
//	}
func Or(channels ...<-chan struct{}) <-chan struct{} {
	switch len(channels) {
	case 0:
		return nil
	case 1:
		return channels[0]
	}

	orDone := make(chan struct{})
	go func() {
		defer close(orDone)

		switch len(channels) {
		case 2:
			select {
			case <-channels[0]:
			case <-channels[1]:
			}
		default:
			select {
			case <-channels[0]:
			case <-channels[1]:
			case <-channels[2]:
			case <-Or(append(channels[3:], orDone)...):
			}
		}
	}()
	return orDone
}

// OrDone wraps a channel read with context cancellation.
// Returns a channel that yields values from in until ctx is done or in is closed.
//
// Book reference: p. 119 - The or-done-channel pattern.
//
// This simplifies the common pattern of:
//
//	for {
//	    select {
//	    case <-ctx.Done():
//	        return
//	    case v, ok := <-ch:
//	        if !ok { return }
//	        // use v
//	    }
//	}
//
// Into:
//
//	for v := range OrDone(ctx, ch) {
//	    // use v
//	}
func OrDone[T any](ctx context.Context, ch <-chan T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// Tee duplicates a channel stream to two output channels.
// Both output channels receive the same values from in.
// Closes both outputs when done closes or in is exhausted.
//
// Book reference: p. 120 - The tee-channel pattern.
//
// This enables non-intrusive fan-out for observability:
//
//	done := Or(ctx.Done(), stopCh)
//	mainCh, metricsCh := Tee(done, resultCh)
//	go processResults(mainCh)      // primary consumer
//	go sampleMetrics(metricsCh)    // lightweight observer
//
// Note: Both consumers must drain their channels to prevent blocking.
// For slow consumers, consider adding buffering or sampling.
func Tee[T any](done <-chan struct{}, in <-chan T) (<-chan T, <-chan T) {
	out1 := make(chan T)
	out2 := make(chan T)

	go func() {
		defer close(out1)
		defer close(out2)

		for v := range in {
			// Local copies for the select cases
			o1, o2 := out1, out2

			// Send to both outputs, checking done between each
			for i := 0; i < 2; i++ {
				select {
				case <-done:
					return
				case o1 <- v:
					o1 = nil // Disable this case after sending
				case o2 <- v:
					o2 = nil // Disable this case after sending
				}
			}
		}
	}()

	return out1, out2
}

// ContextDone converts a context to a done channel for use with Or and Tee.
// This is a convenience wrapper since ctx.Done() returns <-chan struct{}.
func ContextDone(ctx context.Context) <-chan struct{} {
	return ctx.Done()
}
