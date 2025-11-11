package logger

import (
	"io"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// BenchmarkZapLogger_Info benchmarks basic Info logging
func BenchmarkZapLogger_Info(b *testing.B) {
	cfg := LoggerConfig{
		Level:          "info",
		Format:         "json",
		EnableSampling: false,
		Development:    false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Info("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "status", Value: "success"},
		)
	}
}

// BenchmarkZapLogger_InfoWithSampling benchmarks Info logging with sampling
func BenchmarkZapLogger_InfoWithSampling(b *testing.B) {
	cfg := LoggerConfig{
		Level:            "info",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    100,
		SampleThereafter: 1000,
		Development:      false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Info("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "status", Value: "success"},
		)
	}
}

// BenchmarkZapLogger_Debug benchmarks Debug logging (high volume)
func BenchmarkZapLogger_Debug(b *testing.B) {
	cfg := LoggerConfig{
		Level:          "debug",
		Format:         "json",
		EnableSampling: false,
		Development:    false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Debug("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "step", Value: "validation"},
		)
	}
}

// BenchmarkZapLogger_DebugWithSampling benchmarks Debug logging with aggressive sampling
func BenchmarkZapLogger_DebugWithSampling(b *testing.B) {
	cfg := LoggerConfig{
		Level:            "debug",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    10,
		SampleThereafter: 1000,
		Development:      false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Debug("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "step", Value: "validation"},
		)
	}
}

// BenchmarkZapLogger_WithFields benchmarks logging with context fields
func BenchmarkZapLogger_WithFields(b *testing.B) {
	cfg := LoggerConfig{
		Level:          "info",
		Format:         "json",
		EnableSampling: false,
		Development:    false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	// Create logger with context fields
	contextLogger := logger.With(
		Field{Key: "component", Value: "pulse"},
		Field{Key: "worker_id", Value: 42},
	)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		contextLogger.Info("processing batch",
			Field{Key: "batch_size", Value: 1000},
			Field{Key: "duration_ms", Value: 150},
		)
	}
}

// BenchmarkZapLogger_ManyFields benchmarks logging with many fields
func BenchmarkZapLogger_ManyFields(b *testing.B) {
	cfg := LoggerConfig{
		Level:          "info",
		Format:         "json",
		EnableSampling: false,
		Development:    false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Info("complex operation",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "operation", Value: "health_check"},
			Field{Key: "status", Value: "success"},
			Field{Key: "duration", Value: time.Millisecond * 100},
			Field{Key: "retries", Value: 0},
			Field{Key: "endpoint", Value: "https://api.example.com"},
			Field{Key: "status_code", Value: 200},
			Field{Key: "response_time_ms", Value: 95},
		)
	}
}

// BenchmarkZapLogger_Error benchmarks error logging (should never be sampled)
func BenchmarkZapLogger_Error(b *testing.B) {
	cfg := LoggerConfig{
		Level:            "info",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    100,
		SampleThereafter: 1000,
		Development:      false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Error("health check failed",
			Field{Key: "entity_id", Value: uint64(i)},
			Field{Key: "error", Value: "connection timeout"},
		)
	}
}

// BenchmarkZapCore_NoOp benchmarks the overhead of a no-op logger
func BenchmarkZapCore_NoOp(b *testing.B) {
	logger := &ZapLogger{zap: zap.NewNop()}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Info("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
		)
	}
}

// BenchmarkZapCore_Discard benchmarks zap with discarded output
func BenchmarkZapCore_Discard(b *testing.B) {
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(io.Discard),
		zapcore.InfoLevel,
	)
	logger := &ZapLogger{zap: zap.New(core)}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		logger.Info("processing entity",
			Field{Key: "entity_id", Value: uint64(i)},
		)
	}
}

// BenchmarkConvertFields benchmarks field conversion overhead
func BenchmarkConvertFields(b *testing.B) {
	fields := []Field{
		{Key: "entity_id", Value: uint64(123)},
		{Key: "status", Value: "success"},
		{Key: "duration", Value: time.Millisecond * 100},
		{Key: "retries", Value: 0},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = convertFields(fields)
	}
}

// BenchmarkParallelLogging benchmarks concurrent logging
func BenchmarkParallelLogging(b *testing.B) {
	cfg := LoggerConfig{
		Level:            "info",
		Format:           "json",
		EnableSampling:   true,
		SampleInitial:    100,
		SampleThereafter: 1000,
		Development:      false,
	}
	logger, _ := NewZapLogger(cfg)
	defer logger.Sync()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			logger.Info("processing entity",
				Field{Key: "entity_id", Value: uint64(i)},
				Field{Key: "status", Value: "success"},
			)
			i++
		}
	})
}
