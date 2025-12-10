// Package logger provides a structured logging interface for CPRA.
//
// The logger package defines a common logging interface that supports both
// traditional string-based logging and structured field-based logging.
// Implementations can use various backends (e.g., zap) while maintaining
// a consistent API.
//
// # Features
//
//   - Structured logging with key-value fields
//   - Multiple log levels (Debug, Info, Warn, Error, Fatal)
//   - Contextual logging via With() for adding fields
//   - Sync support for flushing log buffers
//
// # Implementations
//
// The package provides a zap-based implementation (ZapLogger) that offers:
//   - High performance structured logging
//   - Configurable output formats (JSON, console)
//   - Log level filtering
//   - Sampling for high-volume scenarios
//
// # Example
//
//	logger := logger.NewZapLogger(logger.DefaultConfig())
//	defer logger.Sync()
//
//	logger.Info("Application started",
//		logger.Field{Key: "version", Value: "1.0.0"},
//		logger.Field{Key: "port", Value: 8080},
//	)
package logger

// Logger interface for CPRA - maintains backward compatibility
// while enabling structured logging with sampling.
//
// Logger implementations should be safe for concurrent use. The With() method
// returns a new logger instance with additional fields, allowing contextual
// logging without mutating the original logger.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	With(fields ...Field) Logger
	Sync() error
}

// Field represents a structured log field
type Field struct {
	Value interface{}
	Key   string
}
