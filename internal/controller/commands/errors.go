package commands

import "errors"

// Sentinel errors for command execution.
var (
	// ErrNotImplemented indicates a command is not yet implemented.
	ErrNotImplemented = errors.New("command not implemented")

	// ErrEntityNotFound indicates the requested entity doesn't exist.
	ErrEntityNotFound = errors.New("entity not found")

	// ErrQueueFull indicates the command channel is full.
	ErrQueueFull = errors.New("command queue full")

	// ErrQueueClosed indicates the command channel is closed.
	ErrQueueClosed = errors.New("command queue closed")

	// ErrInvalidSpec indicates the command spec failed validation.
	ErrInvalidSpec = errors.New("invalid command specification")
)
