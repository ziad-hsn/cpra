package controller

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// LogLevel represents different logging levels
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
	LogLevelFatal
)

var logLevelNames = map[LogLevel]string{
	LogLevelDebug: "DEBUG",
	LogLevelInfo:  "INFO",
	LogLevelWarn:  "WARN",
	LogLevelError: "ERROR",
	LogLevelFatal: "FATAL",
}

var logLevelColors = map[LogLevel]string{
	LogLevelDebug: "\033[36m", // Cyan
	LogLevelInfo:  "\033[32m", // Green
	LogLevelWarn:  "\033[33m", // Yellow
	LogLevelError: "\033[31m", // Red
	LogLevelFatal: "\033[35m", // Magenta
}

const colorReset = "\033[0m"

// Logger provides structured logging with levels and context
type Logger struct {
	level       LogLevel
	component   string
	enableColor bool
	debugMode   bool
	prodMode    bool
	file        *os.File
}

// NewLogger creates a new logger instance
func NewLogger(component string, debugMode bool) *Logger {
	level := LogLevelInfo
	if debugMode {
		level = LogLevelDebug
	}

	// Check environment for production mode
	prodMode := strings.ToLower(os.Getenv("CPRA_ENV")) == "production"
	if prodMode {
		level = LogLevelWarn // More restrictive in production
	}

	// Enable colors for terminal output (disable in production)
	enableColor := !prodMode && isTerminal()

	logger := &Logger{
		level:       level,
		component:   component,
		enableColor: enableColor,
		debugMode:   debugMode,
		prodMode:    prodMode,
	}

	// Setup file logging for production
	if prodMode {
		logger.setupFileLogging()
	}

	return logger
}

// isTerminal checks if we're running in a terminal
func isTerminal() bool {
	fileInfo, _ := os.Stdout.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// setupFileLogging configures file output for production
func (l *Logger) setupFileLogging() {
	logFile := fmt.Sprintf("cpra-%s.log", time.Now().Format("2006-01-02"))
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		return
	}
	l.file = file
}

// Close closes the log file if open
func (l *Logger) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

// formatMessage formats a log message with timestamp, level, and component
func (l *Logger) formatMessage(level LogLevel, msg string, args ...interface{}) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	levelName := logLevelNames[level]

	formattedMsg := fmt.Sprintf(msg, args...)

	if l.enableColor {
		color := logLevelColors[level]
		return fmt.Sprintf("%s [%s%s%s] [%s] %s",
			timestamp, color, levelName, colorReset, l.component, formattedMsg)
	}

	return fmt.Sprintf("%s [%s] [%s] %s",
		timestamp, levelName, l.component, formattedMsg)
}

// log writes a message at the specified level
func (l *Logger) log(level LogLevel, msg string, args ...interface{}) {
	if level < l.level {
		return
	}

	formatted := l.formatMessage(level, msg, args...)

	// Always output to stdout/stderr
	if level >= LogLevelError {
		fmt.Fprintf(os.Stderr, "%s\n", formatted)
	} else {
		fmt.Fprintf(os.Stdout, "%s\n", formatted)
	}

	// Also write to file in production
	if l.file != nil {
		fmt.Fprintf(l.file, "%s\n", formatted)
		l.file.Sync() // Ensure immediate write
	}
}

// Debug logs a debug message (only in debug mode)
func (l *Logger) Debug(msg string, args ...interface{}) {
	l.log(LogLevelDebug, msg, args...)
}

// Info logs an info message
func (l *Logger) Info(msg string, args ...interface{}) {
	l.log(LogLevelInfo, msg, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, args ...interface{}) {
	l.log(LogLevelWarn, msg, args...)
}

// Error logs an error message
func (l *Logger) Error(msg string, args ...interface{}) {
	l.log(LogLevelError, msg, args...)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(msg string, args ...interface{}) {
	l.log(LogLevelFatal, msg, args...)
	os.Exit(1)
}

// WithContext creates a new logger with additional context
func (l *Logger) WithContext(context string) *Logger {
	return &Logger{
		level:       l.level,
		component:   fmt.Sprintf("%s:%s", l.component, context),
		enableColor: l.enableColor,
		debugMode:   l.debugMode,
		prodMode:    l.prodMode,
		file:        l.file,
	}
}

// LogSystemPerformance logs system performance metrics
func (l *Logger) LogSystemPerformance(component string, duration time.Duration, entitiesProcessed int) {
	if l.debugMode {
		rate := float64(entitiesProcessed) / duration.Seconds()
		l.Debug("Performance: %s processed %d entities in %v (%.1f/sec)",
			component, entitiesProcessed, duration, rate)
	}
}

// LogEntityOperation logs entity-level operations in debug mode
func (l *Logger) LogEntityOperation(operation string, entityID uint64, details string) {
	if l.debugMode {
		l.Debug("Entity[%d] %s: %s", entityID, operation, details)
	}
}

// LogWorkerPool logs worker pool statistics
func (l *Logger) LogWorkerPool(poolName string, stats map[string]interface{}) {
	if l.debugMode {
		l.Debug("WorkerPool[%s] stats: %+v", poolName, stats)
	} else if l.level <= LogLevelInfo {
		if processed, ok := stats["processed"].(int64); ok {
			if rate, ok := stats["rate"].(float64); ok {
				l.Info("Pool %s: %d jobs (%.1f/sec)", poolName, processed, rate)
			}
		}
	}
}

// LogComponentState logs component state changes
func (l *Logger) LogComponentState(entityID uint32, component string, state string) {
	if l.debugMode {
		l.Debug("Entity[%d] %s -> %s", entityID, component, state)
	}
}

// LogChannelState logs channel buffer states
func (l *Logger) LogChannelState(channelName string, depth, capacity int) {
	if l.debugMode {
		utilization := float64(depth) / float64(capacity) * 100
		l.Debug("Channel[%s] depth: %d/%d (%.1f%% full)",
			channelName, depth, capacity, utilization)
	} else if depth == capacity {
		l.Warn("Channel[%s] is full (%d/%d)", channelName, depth, capacity)
	}
}

// LogJobExecution logs job execution details
func (l *Logger) LogJobExecution(jobType string, entityID uint64, duration time.Duration, success bool) {
	if l.debugMode {
		status := "SUCCESS"
		if !success {
			status = "FAILED"
		}
		l.Debug("Job[%s] Entity[%d] %s in %v", jobType, entityID, status, duration)
	}
}

// Global logger instances for different components
var (
	SystemLogger     *Logger
	SchedulerLogger  *Logger
	DispatchLogger   *Logger
	ResultLogger     *Logger
	WorkerPoolLogger *Logger
	EntityLogger     *Logger
)

// InitializeLoggers sets up all component loggers
func InitializeLoggers(debugMode bool) {
	SystemLogger = NewLogger("SYSTEM", debugMode)
	SchedulerLogger = NewLogger("SCHEDULER", debugMode)
	DispatchLogger = NewLogger("DISPATCH", debugMode)
	ResultLogger = NewLogger("RESULT", debugMode)
	WorkerPoolLogger = NewLogger("WORKER", debugMode)
	EntityLogger = NewLogger("ENTITY", debugMode)
}

// CloseLoggers closes all logger files
func CloseLoggers() {
	loggers := []*Logger{
		SystemLogger, SchedulerLogger, DispatchLogger,
		ResultLogger, WorkerPoolLogger, EntityLogger,
	}

	for _, logger := range loggers {
		if logger != nil {
			logger.Close()
		}
	}
}
