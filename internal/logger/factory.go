package logger

import (
	"os"
	"strconv"
	"strings"
)

// NewLoggerFromEnv creates a logger based on environment variables
func NewLoggerFromEnv() (Logger, error) {
	cfg := configFromEnv()
	return NewZapLogger(cfg)
}

// NewLoggerWithComponent creates a logger with a component field pre-set
func NewLoggerWithComponent(component string) (Logger, error) {
	cfg := configFromEnv()
	logger, err := NewZapLogger(cfg)
	if err != nil {
		return nil, err
	}

	return logger.With(Field{Key: "component", Value: component}), nil
}

// configFromEnv builds LoggerConfig from environment variables
func configFromEnv() LoggerConfig {
	cfg := DefaultConfig()

	// Check if we're in development based on environment
	isDev := strings.ToLower(os.Getenv("CPRA_ENV")) != "production"
	if isDev {
		cfg = DevelopmentConfig()
	}

	// Override with specific env vars if set
	if level := os.Getenv("CPRA_LOG_LEVEL"); level != "" {
		cfg.Level = level
	}

	if format := os.Getenv("CPRA_LOG_FORMAT"); format != "" {
		cfg.Format = format
	}

	if sampling := os.Getenv("CPRA_LOG_SAMPLING"); sampling != "" {
		cfg.EnableSampling = strings.ToLower(sampling) == "true"
	}

	if initial := os.Getenv("CPRA_LOG_SAMPLE_INITIAL"); initial != "" {
		if val, err := strconv.Atoi(initial); err == nil {
			cfg.SampleInitial = val
		}
	}

	if thereafter := os.Getenv("CPRA_LOG_SAMPLE_THEREAFTER"); thereafter != "" {
		if val, err := strconv.Atoi(thereafter); err == nil {
			cfg.SampleThereafter = val
		}
	}

	if dev := os.Getenv("CPRA_LOG_DEVELOPMENT"); dev != "" {
		cfg.Development = strings.ToLower(dev) == "true"
	}

	return cfg
}
