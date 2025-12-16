// Package controller provides logging infrastructure using zap SugaredLogger.
//
// This file defines global loggers for different components of the controller.
// All loggers use zap's SugaredLogger for printf-style logging with methods
// like Infof, Debugf, Warnf, Errorf, and Fatalf.
package controller

import (
	"cpra/internal/logger"

	"go.uber.org/zap"
)

// Global logger instances for different components.
// Use printf-style methods: Infof, Debugf, Warnf, Errorf, Fatalf
// Or structured methods: Infow, Debugw, etc. with key-value pairs
var (
	SystemLogger     *zap.SugaredLogger
	SchedulerLogger  *zap.SugaredLogger
	DispatchLogger   *zap.SugaredLogger
	ResultLogger     *zap.SugaredLogger
	WorkerPoolLogger *zap.SugaredLogger
	EntityLogger     *zap.SugaredLogger
)

// InitializeLoggers sets up all component loggers with zap SugaredLogger.
// In debug mode, uses development config (console output, debug level).
// In production mode, uses production config (JSON output, info level, sampling).
func InitializeLoggers(debugMode bool) {
	cfg := logger.DefaultConfig()
	if debugMode {
		cfg = logger.DevelopmentConfig()
	}

	// Create component-specific loggers
	// Each logger has a "component" field pre-set for filtering
	var err error

	SystemLogger, err = logger.NewSugaredLoggerWithComponent("SYSTEM", cfg)
	if err != nil {
		panic("failed to create SystemLogger: " + err.Error())
	}

	SchedulerLogger, err = logger.NewSugaredLoggerWithComponent("SCHEDULER", cfg)
	if err != nil {
		panic("failed to create SchedulerLogger: " + err.Error())
	}

	DispatchLogger, err = logger.NewSugaredLoggerWithComponent("DISPATCH", cfg)
	if err != nil {
		panic("failed to create DispatchLogger: " + err.Error())
	}

	ResultLogger, err = logger.NewSugaredLoggerWithComponent("RESULT", cfg)
	if err != nil {
		panic("failed to create ResultLogger: " + err.Error())
	}

	WorkerPoolLogger, err = logger.NewSugaredLoggerWithComponent("WORKER", cfg)
	if err != nil {
		panic("failed to create WorkerPoolLogger: " + err.Error())
}

	EntityLogger, err = logger.NewSugaredLoggerWithComponent("ENTITY", cfg)
	if err != nil {
		panic("failed to create EntityLogger: " + err.Error())
	}
}

// CloseLoggers flushes all logger buffers.
// Should be called before program exit to ensure all logs are written.
func CloseLoggers() {
	loggers := []*zap.SugaredLogger{
		SystemLogger, SchedulerLogger, DispatchLogger,
		ResultLogger, WorkerPoolLogger, EntityLogger,
	}

	for _, l := range loggers {
		if l != nil {
			_ = l.Sync()
		}
	}
}
