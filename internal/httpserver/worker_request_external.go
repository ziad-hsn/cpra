//go:build externaljobs

package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

var (
	errWorkerBodyEncoding    = errors.New("unsupported worker request encoding")
	errWorkerBodyLimit       = errors.New("worker request body limit exceeded")
	errWorkerBodyInvalid     = errors.New("invalid worker request body")
	errWorkerBodyUnavailable = errors.New("worker request read unavailable")
)

// Call only after authentication and bounded admission. The request must have
// a deadline; cancellation interrupts a stalled body read before releasing it.
func readWorkerProtocolBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	ctx := r.Context()
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errWorkerBodyUnavailable
	}
	control := http.NewResponseController(w)
	if err := control.SetReadDeadline(deadline); err != nil {
		return nil, errWorkerBodyUnavailable
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		_ = control.SetReadDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = control.SetReadDeadline(time.Time{})
	}()
	// Close before restoring the read deadline: net/http may drain an unread
	// body during Close, including after a canceled partial request.
	defer r.Body.Close()
	if encoding := r.Header.Values("Content-Encoding"); len(encoding) > 1 || len(encoding) == 1 && encoding[0] != "identity" {
		return nil, errWorkerBodyEncoding
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil && ctx.Err() == nil {
		return raw, nil
	}
	clear(raw)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	var exceeded *http.MaxBytesError
	if errors.As(err, &exceeded) {
		return nil, errWorkerBodyLimit
	}
	return nil, errWorkerBodyInvalid
}
