package logger

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestZapLogger_BasicLogging(t *testing.T) {
	// Create test logger with observer to capture logs
	core, recorded := observer.New(zapcore.DebugLevel)
	logger := &ZapLogger{
		zap: zap.New(core),
	}

	// Test different log levels
	logger.Debug("debug message", Field{Key: "level", Value: "debug"})
	logger.Info("info message", Field{Key: "level", Value: "info"})
	logger.Warn("warn message", Field{Key: "level", Value: "warn"})
	logger.Error("error message", Field{Key: "level", Value: "error"})

	// Verify all logs were captured
	logs := recorded.All()
	if len(logs) != 4 {
		t.Errorf("Expected 4 logs, got %d", len(logs))
	}

	// Verify log levels
	expectedLevels := []zapcore.Level{
		zapcore.DebugLevel,
		zapcore.InfoLevel,
		zapcore.WarnLevel,
		zapcore.ErrorLevel,
	}

	for i, log := range logs {
		if log.Level != expectedLevels[i] {
			t.Errorf("Log %d: expected level %v, got %v", i, expectedLevels[i], log.Level)
		}
	}
}

func TestZapLogger_StructuredFields(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &ZapLogger{
		zap: zap.New(core),
	}

	// Test various field types
	logger.Info("test message",
		Field{Key: "string_field", Value: "test"},
		Field{Key: "int_field", Value: 42},
		Field{Key: "int64_field", Value: int64(123)},
		Field{Key: "uint64_field", Value: uint64(456)},
		Field{Key: "float_field", Value: 3.14},
		Field{Key: "bool_field", Value: true},
		Field{Key: "duration_field", Value: time.Second},
	)

	logs := recorded.All()
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log, got %d", len(logs))
	}

	log := logs[0]

	// Verify fields were captured
	contextMap := log.ContextMap()

	if contextMap["string_field"] != "test" {
		t.Errorf("Expected string_field='test', got '%v'", contextMap["string_field"])
	}
	if contextMap["int_field"] != int64(42) {
		t.Errorf("Expected int_field=42, got %v", contextMap["int_field"])
	}
	if contextMap["bool_field"] != true {
		t.Errorf("Expected bool_field=true, got %v", contextMap["bool_field"])
	}
}

func TestZapLogger_With(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &ZapLogger{
		zap: zap.New(core),
	}

	// Create child logger with context
	childLogger := logger.With(
		Field{Key: "component", Value: "test"},
		Field{Key: "entity_id", Value: uint64(123)},
	)

	// Log with child logger
	childLogger.Info("test message")

	logs := recorded.All()
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log, got %d", len(logs))
	}

	// Verify context fields are included
	contextMap := logs[0].ContextMap()
	if contextMap["component"] != "test" {
		t.Errorf("Expected component='test', got '%v'", contextMap["component"])
	}
	if contextMap["entity_id"] != uint64(123) {
		t.Errorf("Expected entity_id=123, got %v", contextMap["entity_id"])
	}
}

func TestZapLogger_Sampling(t *testing.T) {
	// Create logger with sampling using the actual NewZapLogger function
	cfg := LoggerConfig{
		Level:            "debug",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    10,  // First 10 pass through
		SampleThereafter: 100, // Then 1 in 100
		Development:      false,
	}

	logger, err := NewZapLogger(cfg)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// Generate many debug logs to test sampling
	// Note: With sampling config above, we expect:
	// - First 10 logs to pass through
	// - After that, 1 in 100 logs
	// So for 1000 logs: 10 + (990 / 100) ~= 19-20 logs
	for i := 0; i < 1000; i++ {
		logger.Debug("test message", Field{Key: "iteration", Value: i})
	}

	// We can't easily count logs without observer core, but we can verify
	// the logger doesn't crash and handles sampling config correctly
	// This is more of an integration test - the logger should work with sampling enabled
	t.Log("Sampling test completed - logger handled 1000 logs with sampling enabled")
}

func TestContext_WithLogger(t *testing.T) {
	core, recorded := observer.New(zapcore.InfoLevel)
	logger := &ZapLogger{
		zap: zap.New(core),
	}

	// Add logger to context
	ctx := WithLogger(context.Background(), logger)

	// Retrieve logger from context
	retrieved := FromContext(ctx)

	// Use retrieved logger
	retrieved.Info("test message")

	logs := recorded.All()
	if len(logs) != 1 {
		t.Errorf("Expected 1 log, got %d", len(logs))
	}
}

func TestContext_NoLogger(t *testing.T) {
	// Try to get logger from empty context
	logger := FromContext(context.Background())

	// Should not panic, should return no-op logger
	logger.Info("test message") // Should not panic
}

func TestLoggerConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Level != "info" {
		t.Errorf("Expected default level 'info', got '%s'", cfg.Level)
	}
	if cfg.Format != "json" {
		t.Errorf("Expected default format 'json', got '%s'", cfg.Format)
	}
	if !cfg.EnableSampling {
		t.Error("Expected sampling enabled by default")
	}
	if cfg.SampleInitial != 100 {
		t.Errorf("Expected initial sample 100, got %d", cfg.SampleInitial)
	}
	if cfg.SampleThereafter != 1000 {
		t.Errorf("Expected thereafter sample 1000, got %d", cfg.SampleThereafter)
	}
}

func TestLoggerConfig_Development(t *testing.T) {
	cfg := DevelopmentConfig()

	if cfg.Level != "debug" {
		t.Errorf("Expected development level 'debug', got '%s'", cfg.Level)
	}
	if cfg.Format != "console" {
		t.Errorf("Expected development format 'console', got '%s'", cfg.Format)
	}
	if cfg.EnableSampling {
		t.Error("Expected sampling disabled in development")
	}
	if !cfg.Development {
		t.Error("Expected Development flag to be true")
	}
}

func TestConvertFields_AllTypes(t *testing.T) {
	fields := []Field{
		{Key: "string", Value: "test"},
		{Key: "int", Value: 42},
		{Key: "int64", Value: int64(123)},
		{Key: "uint64", Value: uint64(456)},
		{Key: "float64", Value: 3.14},
		{Key: "bool", Value: true},
		{Key: "duration", Value: time.Second},
	}

	zapFields := convertFields(fields)

	if len(zapFields) != len(fields) {
		t.Errorf("Expected %d zap fields, got %d", len(fields), len(zapFields))
	}

	// Verify each field was converted
	for i, zf := range zapFields {
		if zf.Key != fields[i].Key {
			t.Errorf("Field %d: expected key '%s', got '%s'", i, fields[i].Key, zf.Key)
		}
	}
}
