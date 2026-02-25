// Package config provides centralized environment variable configuration for CPRA.
// All environment variables are loaded once at startup, validated, and made available
// throughout the application. This follows 12-factor app principles.
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
)

// EnvConfig holds all environment-based configuration for CPRA.
// Values are loaded once at startup via Load() and should not be modified.
type EnvConfig struct {
	// Environment
	Env   string // CPRA_ENV: "production" or "development" (default: development)
	Debug bool   // CPRA_DEBUG: enables debug logging (default: false)

	// Logging
	LogLevel            string `validate:"oneof=debug info warn error fatal"` // CPRA_LOG_LEVEL
	LogFormat           string `validate:"oneof=json console"`                // CPRA_LOG_FORMAT
	LogSampling         bool   // CPRA_LOG_SAMPLING
	LogSampleInitial    int    // CPRA_LOG_SAMPLE_INITIAL
	LogSampleThereafter int    // CPRA_LOG_SAMPLE_THEREAFTER
	LogDevelopment      bool   // CPRA_LOG_DEVELOPMENT

	// Worker Pool
	WorkerPerCore   int     `validate:"min=1"`             // CPRA_WORKER_PER_CORE
	WorkerHeadroom  float64 `validate:"gte=0.10,lte=0.95"` // CPRA_WORKER_HEADROOM
	WorkerMemBudget int     `validate:"min=1024"`          // CPRA_WORKER_MEM_BUDGET_BYTES
	MaxWorkers      int     `validate:"min=0"`             // CPRA_MAX_WORKERS
	QueueDriftLog   bool    // CPRA_QUEUE_DRIFT_LOG

	// Sizing (M/M/c queueing theory parameters)
	SizingTauMS       int     `validate:"min=0"`           // CPRA_SIZING_TAU_MS
	SizingSLOMS       int     `validate:"min=0"`           // CPRA_SIZING_SLO_MS
	SizingHeadroomPct float64 `validate:"gte=0.0,lte=1.0"` // CPRA_SIZING_HEADROOM_PCT: 0.0-1.0

	// Health endpoints
	HealthPort     int    `validate:"min=1,max=65535"` // CPRA_HEALTH_PORT
	HealthPath     string // CPRA_HEALTH_PATH
	LivenessPath   string // CPRA_LIVENESS_PATH
	ReadinessPath  string // CPRA_READINESS_PATH
	HealthCacheTTL int    `validate:"min=1"` // CPRA_HEALTH_CACHE_TTL

	// mTLS configuration
	MTLSEnabled        bool   // CPRA_MTLS_ENABLED
	MTLSCertFile       string `validate:"required_if=MTLSEnabled true"` // CPRA_MTLS_CERT_FILE
	MTLSKeyFile        string `validate:"required_if=MTLSEnabled true"` // CPRA_MTLS_KEY_FILE
	MTLSCAFile         string `validate:"required_if=MTLSEnabled true"` // CPRA_MTLS_CA_FILE
	MTLSReloadEnabled  bool   // CPRA_MTLS_RELOAD_ENABLED
	MTLSReloadInterval int    `validate:"min=10"` // CPRA_MTLS_RELOAD_INTERVAL
	MTLSClientCert     string // CPRA_MTLS_CLIENT_CERT
	MTLSClientKey      string // CPRA_MTLS_CLIENT_KEY

	// OpenTelemetry configuration
	OTLPEnabled    bool    // CPRA_OTLP_ENABLED
	OTLPEndpoint   string  // CPRA_OTLP_ENDPOINT
	OTLPInsecure   bool    // CPRA_OTLP_INSECURE
	ServiceName    string  // CPRA_SERVICE_NAME
	ServiceVersion string  // CPRA_SERVICE_VERSION
	TraceRatio     float64 `validate:"gte=0.0,lte=1.0"` // CPRA_TRACE_RATIO

	// Computed values (not from env vars directly)
	IsProduction bool // Computed: Env == "production"
	NumCPU       int  // Computed: runtime.NumCPU()
}

// Default constants
const (
	defaultWorkerPerCore   = 500 // Increased for 1M monitors
	defaultWorkerHeadroom  = 0.75
	defaultWorkerMemBudget = 16 * 1024 // 16KB
	defaultSizingHeadroom  = 0.15      // 15%
	defaultLogSampleInit   = 100
	defaultLogSampleAfter  = 1000

	// Platform-specific max worker defaults (based on ephemeral port limits)
	// Linux: ~28K ports available, cap at 25K with headroom
	// Windows: ~16K ports available, cap at 10K with headroom
	defaultMaxWorkersLinux   = 25000
	defaultMaxWorkersWindows = 10000
)

// Load reads all environment variables and returns an EnvConfig with defaults applied.
// This should be called once at application startup.
func Load() *EnvConfig {
	env := strings.ToLower(getEnvString("CPRA_ENV", "development"))
	isProduction := env == "production"

	cfg := &EnvConfig{
		// Environment
		Env:          env,
		Debug:        getEnvBool("CPRA_DEBUG", false),
		IsProduction: isProduction,
		NumCPU:       runtime.NumCPU(),

		// Logging - defaults depend on environment
		LogLevel:            getEnvString("CPRA_LOG_LEVEL", ""),
		LogFormat:           getEnvString("CPRA_LOG_FORMAT", ""),
		LogSampling:         getEnvBoolDefault("CPRA_LOG_SAMPLING", isProduction),
		LogSampleInitial:    getEnvInt("CPRA_LOG_SAMPLE_INITIAL", defaultLogSampleInit, 0),
		LogSampleThereafter: getEnvInt("CPRA_LOG_SAMPLE_THEREAFTER", defaultLogSampleAfter, 0),
		LogDevelopment:      getEnvBoolDefault("CPRA_LOG_DEVELOPMENT", !isProduction),

		// Worker Pool
		WorkerPerCore:   getEnvInt("CPRA_WORKER_PER_CORE", defaultWorkerPerCore, 1),
		WorkerHeadroom:  getEnvFloat("CPRA_WORKER_HEADROOM", defaultWorkerHeadroom, 0.1, 0.95),
		WorkerMemBudget: getEnvInt("CPRA_WORKER_MEM_BUDGET_BYTES", defaultWorkerMemBudget, 1024),
		MaxWorkers:      getEnvInt("CPRA_MAX_WORKERS", getDefaultMaxWorkers(), 0),
		QueueDriftLog:   getEnvBool("CPRA_QUEUE_DRIFT_LOG", false),

		// Sizing
		SizingTauMS:       getEnvInt("CPRA_SIZING_TAU_MS", 0, 0),
		SizingSLOMS:       getEnvInt("CPRA_SIZING_SLO_MS", 0, 0),
		SizingHeadroomPct: getEnvHeadroomPct("CPRA_SIZING_HEADROOM_PCT", defaultSizingHeadroom),

		// Health endpoints
		HealthPort:     getEnvInt("CPRA_HEALTH_PORT", 8080, 1),
		HealthPath:     getEnvString("CPRA_HEALTH_PATH", ""),
		LivenessPath:   getEnvString("CPRA_LIVENESS_PATH", "/healthz"),
		ReadinessPath:  getEnvString("CPRA_READINESS_PATH", "/readyz"),
		HealthCacheTTL: getEnvInt("CPRA_HEALTH_CACHE_TTL", 30, 1),

		// mTLS configuration
		MTLSEnabled:        getEnvBool("CPRA_MTLS_ENABLED", false),
		MTLSCertFile:       getEnvString("CPRA_MTLS_CERT_FILE", ""),
		MTLSKeyFile:        getEnvString("CPRA_MTLS_KEY_FILE", ""),
		MTLSCAFile:         getEnvString("CPRA_MTLS_CA_FILE", ""),
		MTLSReloadEnabled:  getEnvBoolDefault("CPRA_MTLS_RELOAD_ENABLED", true),
		MTLSReloadInterval: getEnvInt("CPRA_MTLS_RELOAD_INTERVAL", 300, 10),
		MTLSClientCert:     getEnvString("CPRA_MTLS_CLIENT_CERT", ""),
		MTLSClientKey:      getEnvString("CPRA_MTLS_CLIENT_KEY", ""),

		// OpenTelemetry configuration
		OTLPEnabled:    getEnvBool("CPRA_OTLP_ENABLED", false),
		OTLPEndpoint:   getEnvString("CPRA_OTLP_ENDPOINT", "localhost:4317"),
		OTLPInsecure:   getEnvBoolDefault("CPRA_OTLP_INSECURE", true),
		ServiceName:    getEnvString("CPRA_SERVICE_NAME", "cpra"),
		ServiceVersion: getEnvString("CPRA_SERVICE_VERSION", "unknown"),
		TraceRatio:     getEnvFloat("CPRA_TRACE_RATIO", 0.1, 0.0, 1.0),
	}

	// Apply CPRA_DEBUG to log level if set and no explicit level
	if cfg.Debug && cfg.LogLevel == "" {
		cfg.LogLevel = "debug"
	}

	// Apply environment-based defaults for empty log settings
	// Development: info level (debug only via --debug flag)
	// Production: warn level (minimal logging, only issues)
	if cfg.LogLevel == "" {
		if isProduction {
			cfg.LogLevel = "warn"
		} else {
			cfg.LogLevel = "info"
		}
	}
	if cfg.LogFormat == "" {
		if isProduction {
			cfg.LogFormat = "json"
		} else {
			cfg.LogFormat = "console"
		}
	}

	// Normalize strings for validation
	cfg.LogLevel = strings.ToLower(cfg.LogLevel)
	cfg.LogFormat = strings.ToLower(cfg.LogFormat)

	return cfg
}

// Validate checks all configuration values and returns an error if any are invalid.
// Validate checks all configuration values using struct tags.
func (c *EnvConfig) Validate() error {
	validate := validator.New()
	return validate.Struct(c)
}

// ConfigLine represents a single configuration entry for logging
type ConfigLine struct {
	Key      string
	Value    string
	Default  string
	IsCustom bool
}

// GetConfigLines returns all configuration as structured lines for logging.
// Lines where IsCustom is true indicate non-default values.
func (c *EnvConfig) GetConfigLines() []ConfigLine {
	lines := []ConfigLine{
		// Environment
		{Key: "CPRA_ENV", Value: c.Env, Default: "development", IsCustom: c.Env != "development"},
		{Key: "CPRA_DEBUG", Value: fmt.Sprintf("%v", c.Debug), Default: "false", IsCustom: c.Debug},

		// Logging
		{Key: "CPRA_LOG_LEVEL", Value: c.LogLevel, Default: "info/debug", IsCustom: os.Getenv("CPRA_LOG_LEVEL") != ""},
		{Key: "CPRA_LOG_FORMAT", Value: c.LogFormat, Default: "json/console", IsCustom: os.Getenv("CPRA_LOG_FORMAT") != ""},
		{Key: "CPRA_LOG_SAMPLING", Value: fmt.Sprintf("%v", c.LogSampling), Default: "true (prod)", IsCustom: os.Getenv("CPRA_LOG_SAMPLING") != ""},
		{Key: "CPRA_LOG_SAMPLE_INITIAL", Value: fmt.Sprintf("%d", c.LogSampleInitial), Default: "100", IsCustom: c.LogSampleInitial != defaultLogSampleInit},
		{Key: "CPRA_LOG_SAMPLE_THEREAFTER", Value: fmt.Sprintf("%d", c.LogSampleThereafter), Default: "1000", IsCustom: c.LogSampleThereafter != defaultLogSampleAfter},
		{Key: "CPRA_LOG_DEVELOPMENT", Value: fmt.Sprintf("%v", c.LogDevelopment), Default: "based on env", IsCustom: os.Getenv("CPRA_LOG_DEVELOPMENT") != ""},

		// Worker Pool
		{Key: "CPRA_WORKER_PER_CORE", Value: fmt.Sprintf("%d", c.WorkerPerCore), Default: "128", IsCustom: c.WorkerPerCore != defaultWorkerPerCore},
		{Key: "CPRA_WORKER_HEADROOM", Value: fmt.Sprintf("%.2f", c.WorkerHeadroom), Default: "0.75", IsCustom: c.WorkerHeadroom != defaultWorkerHeadroom},
		{Key: "CPRA_WORKER_MEM_BUDGET_BYTES", Value: fmt.Sprintf("%d", c.WorkerMemBudget), Default: "16384", IsCustom: c.WorkerMemBudget != defaultWorkerMemBudget},
		{Key: "CPRA_MAX_WORKERS", Value: fmt.Sprintf("%d", c.MaxWorkers), Default: fmt.Sprintf("%d", getDefaultMaxWorkers()), IsCustom: c.MaxWorkers != getDefaultMaxWorkers()},
		{Key: "CPRA_QUEUE_DRIFT_LOG", Value: fmt.Sprintf("%v", c.QueueDriftLog), Default: "false", IsCustom: c.QueueDriftLog},

		// Sizing
		{Key: "CPRA_SIZING_TAU_MS", Value: fmt.Sprintf("%d", c.SizingTauMS), Default: "0 (auto)", IsCustom: c.SizingTauMS != 0},
		{Key: "CPRA_SIZING_SLO_MS", Value: fmt.Sprintf("%d", c.SizingSLOMS), Default: "0 (auto)", IsCustom: c.SizingSLOMS != 0},
		{Key: "CPRA_SIZING_HEADROOM_PCT", Value: fmt.Sprintf("%.2f", c.SizingHeadroomPct), Default: "0.15", IsCustom: c.SizingHeadroomPct != defaultSizingHeadroom},

		// Health endpoints
		{Key: "CPRA_HEALTH_PORT", Value: fmt.Sprintf("%d", c.HealthPort), Default: "8080", IsCustom: c.HealthPort != 8080},
		{Key: "CPRA_HEALTH_PATH", Value: c.HealthPath, Default: "(empty)", IsCustom: c.HealthPath != ""},
		{Key: "CPRA_LIVENESS_PATH", Value: c.LivenessPath, Default: "/healthz", IsCustom: c.LivenessPath != "/healthz"},
		{Key: "CPRA_READINESS_PATH", Value: c.ReadinessPath, Default: "/readyz", IsCustom: c.ReadinessPath != "/readyz"},
		{Key: "CPRA_HEALTH_CACHE_TTL", Value: fmt.Sprintf("%d", c.HealthCacheTTL), Default: "30", IsCustom: c.HealthCacheTTL != 30},

		// mTLS configuration
		{Key: "CPRA_MTLS_ENABLED", Value: fmt.Sprintf("%v", c.MTLSEnabled), Default: "false", IsCustom: c.MTLSEnabled},
		{Key: "CPRA_MTLS_CERT_FILE", Value: c.MTLSCertFile, Default: "(empty)", IsCustom: c.MTLSCertFile != ""},
		{Key: "CPRA_MTLS_KEY_FILE", Value: c.MTLSKeyFile, Default: "(empty)", IsCustom: c.MTLSKeyFile != ""},
		{Key: "CPRA_MTLS_CA_FILE", Value: c.MTLSCAFile, Default: "(empty)", IsCustom: c.MTLSCAFile != ""},
		{Key: "CPRA_MTLS_RELOAD_ENABLED", Value: fmt.Sprintf("%v", c.MTLSReloadEnabled), Default: "true", IsCustom: !c.MTLSReloadEnabled},
		{Key: "CPRA_MTLS_RELOAD_INTERVAL", Value: fmt.Sprintf("%d", c.MTLSReloadInterval), Default: "300", IsCustom: c.MTLSReloadInterval != 300},
		{Key: "CPRA_MTLS_CLIENT_CERT", Value: c.MTLSClientCert, Default: "(empty)", IsCustom: c.MTLSClientCert != ""},
		{Key: "CPRA_MTLS_CLIENT_KEY", Value: c.MTLSClientKey, Default: "(empty)", IsCustom: c.MTLSClientKey != ""},

		// OpenTelemetry configuration
		{Key: "CPRA_OTLP_ENABLED", Value: fmt.Sprintf("%v", c.OTLPEnabled), Default: "false", IsCustom: c.OTLPEnabled},
		{Key: "CPRA_OTLP_ENDPOINT", Value: c.OTLPEndpoint, Default: "localhost:4317", IsCustom: c.OTLPEndpoint != "localhost:4317"},
		{Key: "CPRA_OTLP_INSECURE", Value: fmt.Sprintf("%v", c.OTLPInsecure), Default: "true", IsCustom: !c.OTLPInsecure},
		{Key: "CPRA_SERVICE_NAME", Value: c.ServiceName, Default: "cpra", IsCustom: c.ServiceName != "cpra"},
		{Key: "CPRA_SERVICE_VERSION", Value: c.ServiceVersion, Default: "unknown", IsCustom: c.ServiceVersion != "unknown"},
		{Key: "CPRA_TRACE_RATIO", Value: fmt.Sprintf("%.2f", c.TraceRatio), Default: "0.10", IsCustom: c.TraceRatio != 0.1},
	}

	// Add computed values
	lines = append(lines, ConfigLine{
		Key:      "NumCPU",
		Value:    fmt.Sprintf("%d", c.NumCPU),
		Default:  "runtime",
		IsCustom: false,
	})

	return lines
}

// String returns a compact string representation of the config for logging
func (c *EnvConfig) String() string {
	return fmt.Sprintf("env=%s debug=%v workers_per_core=%d max_workers=%d",
		c.Env, c.Debug, c.WorkerPerCore, c.MaxWorkers)
}

// getDefaultMaxWorkers returns the platform-specific default max workers.
// Based on ephemeral port limits: Linux ~28K, Windows ~16K.
// Returns conservative caps with headroom for other system connections.
func getDefaultMaxWorkers() int {
	if runtime.GOOS == "windows" {
		return defaultMaxWorkersWindows // 10,000
	}
	return defaultMaxWorkersLinux // 25,000
}

// --- Helper functions for reading environment variables ---

func getEnvString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(v) == "true" || v == "1"
	}
	return def
}

func getEnvBoolDefault(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return strings.ToLower(v) == "true" || v == "1"
}

func getEnvInt(key string, def int, min int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= min {
			return parsed
		}
	}
	return def
}

func getEnvFloat(key string, def float64, min float64, max float64) float64 {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			if parsed < min {
				return min
			}
			if max > 0 && parsed > max {
				return max
			}
			return parsed
		}
	}
	return def
}

// getEnvHeadroomPct handles both 0.xx and percentage (e.g., 15) formats
func getEnvHeadroomPct(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			// Accept both 0.xx and percentage like 15 or 15.0
			if f > 1.0 {
				return f / 100.0
			}
			return f
		}
	}
	return def
}
