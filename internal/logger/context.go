package logger

import (
	"context"

	"go.uber.org/zap"
)

// Context key for logger
type loggerKeyType struct{}

var loggerKey = loggerKeyType{}

// WithLogger adds logger to context
func WithLogger(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext retrieves logger from context
// Returns a no-op logger if none found in context
func FromContext(ctx context.Context) Logger {
	if logger, ok := ctx.Value(loggerKey).(Logger); ok {
		return logger
	}
	// Return a no-op logger if none found
	return &ZapLogger{zap: zap.NewNop()}
}
