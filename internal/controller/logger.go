package controller

import (
	"go.uber.org/zap"
)

// Global logger instances (DEPRECATED)
// Kept temporarily empty to satisfy any lingering references in tests,
// but effectively removed from production path.
// TODO: Remove completely after verifying no test usages.
var (
	SystemLogger     *zap.SugaredLogger
	SchedulerLogger  *zap.SugaredLogger
	DispatchLogger   *zap.SugaredLogger
	ResultLogger     *zap.SugaredLogger
	WorkerPoolLogger *zap.SugaredLogger
	EntityLogger     *zap.SugaredLogger
	WatchdogLogger   *zap.SugaredLogger
)

// CloseLoggers flushes all logger buffers.
// Should be called before program exit to ensure all logs are written.
func CloseLoggers() {
	// No-op as loggers are now managed via DI/main.go defer
}
