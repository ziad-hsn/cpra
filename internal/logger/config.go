package logger

// LoggerConfig defines logging configuration
type LoggerConfig struct {
	Level            string `yaml:"level" env:"CPRA_LOG_LEVEL"`
	Format           string `yaml:"format" env:"CPRA_LOG_FORMAT"` // json or console
	EnableSampling   bool   `yaml:"enable_sampling" env:"CPRA_LOG_SAMPLING"`
	SampleInitial    int    `yaml:"sample_initial" env:"CPRA_LOG_SAMPLE_INITIAL"`
	SampleThereafter int    `yaml:"sample_thereafter" env:"CPRA_LOG_SAMPLE_THEREAFTER"`
	Development      bool   `yaml:"development" env:"CPRA_LOG_DEVELOPMENT"`
	// StacktraceLevel is the minimum level that captures a stack trace.
	// Default "dpanic": routine Error/Warn logs stay clean and only genuine
	// panics (DPanic/Panic/Fatal) carry stacks. Accepts a zap level name
	// (debug|info|warn|error|dpanic|panic|fatal) or "none" to disable.
	StacktraceLevel string `yaml:"stacktrace_level" env:"CPRA_LOG_STACKTRACE"`
}

// DefaultConfig returns the default logging configuration
func DefaultConfig() LoggerConfig {
	return LoggerConfig{
		Level:            "info",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    100,  // First 100 messages per level pass through
		SampleThereafter: 1000, // Then 1 in 1000
		Development:      false,
		StacktraceLevel:  "dpanic",
	}
}

// DevelopmentConfig returns development configuration
func DevelopmentConfig() LoggerConfig {
	return LoggerConfig{
		Level:            "debug",
		Format:           "console",
		EnableSampling:   false,
		SampleInitial:    0,
		SampleThereafter: 0,
		Development:      true,
		StacktraceLevel:  "dpanic",
	}
}
