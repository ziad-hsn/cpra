// Package logger provides a structured logging interface for CPRA.
//
// The logger package defines a common logging interface that supports both
// traditional string-based logging and structured field-based logging.
// The primary implementation uses Uber's zap library for high-performance logging.
//
// # Features
//
//   - Structured logging with key-value fields via Logger interface
//   - Printf-style logging via zap.SugaredLogger (Infof, Debugf, etc.)
//   - Multiple log levels (Debug, Info, Warn, Error, Fatal)
//   - Contextual logging via With() for adding fields
//   - Sync support for flushing log buffers
//
// # Usage
//
// For printf-style logging (recommended for most use cases):
//
//	sugar, _ := logger.NewSugaredLogger(logger.DefaultConfig())
//	defer sugar.Sync()
//	sugar.Infof("Server started on port %d", 8080)
//	sugar.Warnf("Connection timeout: %v", err)
//
// For structured logging with fields:
//
//	log, _ := logger.NewZapLogger(logger.DefaultConfig())
//	defer log.Sync()
//	log.Info("Request processed", logger.Field{Key: "duration", Value: 42})
package logger

import "go.uber.org/zap"

// Logger interface for CPRA - for structured logging with fields.
//
// Logger implementations should be safe for concurrent use. The With() method
// returns a new logger instance with additional fields, allowing contextual
// logging without mutating the original logger.
//
// For printf-style logging, use *zap.SugaredLogger directly via NewSugaredLogger().
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	With(fields ...Field) Logger
	Sync() error
}

// Field represents a structured log field for the Logger interface.
type Field struct {
	Value interface{}
	Key   string
}

// SugaredLogger is an alias for zap's SugaredLogger which provides
// printf-style logging methods (Infof, Debugf, Warnf, Errorf, Fatalf)
// as well as loose key-value logging (Infow, Debugw, etc.).
type SugaredLogger = *zap.SugaredLogger
