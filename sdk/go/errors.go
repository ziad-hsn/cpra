package cpra

import (
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var (
	ErrConflict           = errors.New("resource version conflict")
	ErrNotFound           = errors.New("resource not found")
	ErrUnavailable        = errors.New("API unavailable")
	ErrFeatureUnavailable = errors.New("feature unavailable")
	ErrExpired            = errors.New("operation or cursor expired")
	ErrUnauthorized       = errors.New("request unauthorized")
	ErrInvalid            = errors.New("invalid request")
	ErrResponseTooLarge   = errors.New("response exceeds configured byte limit")
	ErrAmbiguous          = errors.New("mutation outcome is uncertain")
	// ErrNotAdmitted means a validated response explicitly guarantees that no
	// target resource or action mutation was submitted. A reservation may exist.
	// This does not enable automatic retries and may also match ErrUnavailable.
	ErrNotAdmitted = errors.New("mutation was not submitted")
)

// Error is an RFC 9457 response. Error() intentionally omits server-controlled
// detail and field values; inspect Problem explicitly when appropriate.
type Error struct {
	StatusCode             int
	Problem                api.Problem
	RequestID, OperationID string
	notAdmitted            bool
}

func (e *Error) Error() string { return fmt.Sprintf("CPRa API returned status %d", e.StatusCode) }
func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotAdmitted:
		return e.notAdmitted
	case ErrConflict:
		return e.StatusCode == 409 || e.StatusCode == 412
	case ErrNotFound:
		return e.StatusCode == 404
	case ErrUnavailable:
		return e.StatusCode == 503
	case ErrFeatureUnavailable:
		return e.StatusCode == 501 || e.Problem.Code == "featureUnavailable"
	case ErrExpired:
		return e.StatusCode == 410
	case ErrUnauthorized:
		return e.StatusCode == 401 || e.StatusCode == 403
	case ErrInvalid:
		return e.StatusCode == 400 || e.StatusCode == 422 || e.StatusCode == 428
	}
	return false
}

// AmbiguousError means the SDK cannot establish whether a mutation committed.
// Inspect the operation handle; never retry a new mutation blindly.
type AmbiguousError struct {
	Cause       error
	OperationID string
}

func (e *AmbiguousError) Error() string        { return ErrAmbiguous.Error() }
func (e *AmbiguousError) Unwrap() error        { return e.Cause }
func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguous }

// TransportError preserves errors.Is cancellation while excluding URL/token
// details from its printable message.
type TransportError struct{ Cause error }

func (e *TransportError) Error() string { return "CPRa transport failed" }
func (e *TransportError) Unwrap() error { return e.Cause }

type localError struct{ error }
