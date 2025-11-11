package logger

// LoggerConfig defines logging configuration
type LoggerConfig struct {
	Level            string `yaml:"level" env:"CPRA_LOG_LEVEL"`
	Format           string `yaml:"format" env:"CPRA_LOG_FORMAT"` // json or console
	EnableSampling   bool   `yaml:"enable_sampling" env:"CPRA_LOG_SAMPLING"`
	SampleInitial    int    `yaml:"sample_initial" env:"CPRA_LOG_SAMPLE_INITIAL"`
	SampleThereafter int    `yaml:"sample_thereafter" env:"CPRA_LOG_SAMPLE_THEREAFTER"`
	Development      bool   `yaml:"development" env:"CPRA_LOG_DEVELOPMENT"`
}

// DefaultConfig returns production-ready default configuration
func DefaultConfig() LoggerConfig {
	return LoggerConfig{
		Level:            "info",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    100,  // First 100 messages per level pass through
		SampleThereafter: 1000, // Then 1 in 1000
		Development:      false,
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
	}
}
