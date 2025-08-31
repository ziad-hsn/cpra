package optimized

import "time"

// Logger interface for structured logging that all optimized systems can use
type Logger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	LogSystemPerformance(name string, duration time.Duration, count int)
	LogComponentState(entityID uint32, component string, action string)
}
