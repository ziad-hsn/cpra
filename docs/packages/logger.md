# Package: logger

## Overview

Package `logger` provides a structured logging interface for CPRA.

The logger package defines a common logging interface that supports both traditional string-based logging and structured field-based logging. The primary implementation uses Uber's zap library for high-performance logging.

## Import Path

```go
import "cpra/internal/logger"
```

## Features

- Structured logging with key-value fields via Logger interface
- Printf-style logging via zap.SugaredLogger (Infof, Debugf, etc.)
- Multiple log levels (Debug, Info, Warn, Error, Fatal)
- Contextual logging via With() for adding fields
- Sync support for flushing log buffers
- Component-tagged loggers for subsystem identification

## Key Types

### Logger Interface

For structured logging with fields:

```go
type Logger interface {
    Debug(msg string, fields ...Field)
    Info(msg string, fields ...Field)
    Warn(msg string, fields ...Field)
    Error(msg string, fields ...Field)
    Fatal(msg string, fields ...Field)
    With(fields ...Field) Logger
    Sync() error
}
```

### Field

Structured log field:

```go
type Field struct {
    Value interface{}
    Key   string
}
```

### SugaredLogger

Alias for zap's SugaredLogger (printf-style):

```go
type SugaredLogger = *zap.SugaredLogger
```

Methods:
- `Debugf(template string, args ...interface{})`
- `Infof(template string, args ...interface{})`
- `Warnf(template string, args ...interface{})`
- `Errorf(template string, args ...interface{})`
- `Fatalf(template string, args ...interface{})`
- `Debugw(msg string, keysAndValues ...interface{})`
- `Infow(msg string, keysAndValues ...interface{})`
- `Warnw(msg string, keysAndValues ...interface{})`
- `Errorw(msg string, keysAndValues ...interface{})`

## Configuration

### Config

Logger configuration:

```go
type Config struct {
    Level       string // "debug", "info", "warn", "error"
    Development bool   // Development mode (pretty printing)
    Encoding    string // "json" or "console"
    OutputPaths []string
    ErrorPaths  []string
}
```

### DefaultConfig

Production defaults:

```go
func DefaultConfig() Config {
    return Config{
        Level:       "info",
        Development: false,
        Encoding:    "json",
        OutputPaths: []string{"stdout"},
        ErrorPaths:  []string{"stderr"},
    }
}
```

### DevelopmentConfig

Development defaults:

```go
func DevelopmentConfig() Config {
    return Config{
        Level:       "debug",
        Development: true,
        Encoding:    "console",
        OutputPaths: []string{"stdout"},
        ErrorPaths:  []string{"stderr"},
    }
}
```

## Factory Functions

### NewSugaredLogger

Creates a printf-style logger:

```go
func NewSugaredLogger(config Config) (*zap.SugaredLogger, error)
```

### NewSugaredLoggerWithComponent

Creates a component-tagged logger:

```go
func NewSugaredLoggerWithComponent(component string, config Config) (*zap.SugaredLogger, error)
```

### NewZapLogger

Creates a structured logger:

```go
func NewZapLogger(config Config) (Logger, error)
```

## Usage Examples

### Printf-style Logging

```go
// Create logger
sugar, err := logger.NewSugaredLogger(logger.DefaultConfig())
if err != nil {
    log.Fatal(err)
}
defer sugar.Sync()

// Log messages
sugar.Infof("Server started on port %d", 8080)
sugar.Warnf("Connection timeout: %v", err)
sugar.Debugf("Processing request %s", requestID)
```

### Structured Logging with Fields

```go
// Create logger
log, err := logger.NewZapLogger(logger.DefaultConfig())
if err != nil {
    panic(err)
}
defer log.Sync()

// Log with fields
log.Info("Request processed",
    logger.Field{Key: "duration_ms", Value: 42},
    logger.Field{Key: "status", Value: 200},
)

// Create contextual logger
reqLogger := log.With(
    logger.Field{Key: "request_id", Value: "abc123"},
    logger.Field{Key: "user_id", Value: 456},
)
reqLogger.Info("User action", logger.Field{Key: "action", Value: "login"})
```

### Component-tagged Logging

```go
// Create component loggers
ctrlLogger, _ := logger.NewSugaredLoggerWithComponent("CONTROLLER", cfg)
pulseLogger, _ := logger.NewSugaredLoggerWithComponent("PULSE", cfg)
queueLogger, _ := logger.NewSugaredLoggerWithComponent("QUEUE", cfg)

// Logs include component tag
ctrlLogger.Infof("Starting controller")  // [CONTROLLER] Starting controller
pulseLogger.Debugf("Check completed")    // [PULSE] Check completed
```

### Key-Value Logging (Sugared)

```go
sugar.Infow("Request completed",
    "method", "GET",
    "path", "/api/health",
    "duration_ms", 42,
    "status", 200,
)
```

## Controller Loggers

The controller package provides pre-configured loggers:

```go
// Initialize loggers
controller.InitializeLoggers(debug)

// Use loggers
controller.SystemLogger.Infof("System message")
controller.WatchdogLogger.Warnf("Watchdog warning")
```

## Performance Considerations

1. **Use SugaredLogger for convenience**: Slightly slower but more ergonomic
2. **Use zap.Logger for hot paths**: Zero-allocation logging
3. **Batch log writes**: Use buffered output for high-volume logging
4. **Sync before exit**: Call Sync() to flush buffers

## Log Levels

| Level | Description |
|-------|-------------|
| Debug | Verbose debugging information |
| Info | Normal operational messages |
| Warn | Warning conditions |
| Error | Error conditions |
| Fatal | Fatal errors (calls os.Exit) |

## Output Formats

### JSON (Production)

```json
{"level":"info","ts":1234567890.123,"caller":"main.go:42","msg":"Server started","port":8080}
```

### Console (Development)

```
2024-01-15T10:30:45.123Z	INFO	main.go:42	Server started	{"port": 8080}
```

## Dependencies

- `go.uber.org/zap`: High-performance logging

