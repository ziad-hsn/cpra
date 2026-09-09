package logger

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapLogger wraps zap.Logger to implement our Logger interface
type ZapLogger struct {
	zap *zap.Logger
}

// NewZapLogger creates a zap logger with sampling
func NewZapLogger(cfg LoggerConfig) (*ZapLogger, error) {
	var zapConfig zap.Config

	if cfg.Development {
		zapConfig = zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		zapConfig = zap.NewProductionConfig()
	}

	// Set log level
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", cfg.Level, err)
	}
	zapConfig.Level = zap.NewAtomicLevelAt(level)

	// Configure format
	if cfg.Format == "console" {
		zapConfig.Encoding = "console"
	} else {
		zapConfig.Encoding = "json"
	}

	// Configure sampling
	if cfg.EnableSampling {
		if cfg.SampleInitial <= 0 || cfg.SampleThereafter <= 0 {
			return nil, fmt.Errorf("invalid sampling config: SampleInitial and SampleThereafter must be > 0 (got %d, %d)", cfg.SampleInitial, cfg.SampleThereafter)
		}
		zapConfig.Sampling = &zap.SamplingConfig{
			Initial:    cfg.SampleInitial,
			Thereafter: cfg.SampleThereafter,
		}
	} else {
		zapConfig.Sampling = nil
	}

	// Stack traces: capture only at the configured level and above.
	// The default (DPanic) keeps routine Error/Warn logs free of stack noise
	// while still capturing stacks for genuine panics. "none" disables stacks.
	opts := []zap.Option{zap.AddCaller()}
	switch strings.ToLower(strings.TrimSpace(cfg.StacktraceLevel)) {
	case "none", "off", "disabled":
		zapConfig.DisableStacktrace = true
	case "":
		opts = append(opts, zap.AddStacktrace(zapcore.DPanicLevel))
	default:
		lvl, lerr := zapcore.ParseLevel(cfg.StacktraceLevel)
		if lerr != nil {
			lvl = zapcore.DPanicLevel
		}
		opts = append(opts, zap.AddStacktrace(lvl))
	}

	logger, err := zapConfig.Build(opts...)
	if err != nil {
		return nil, err
	}

	return &ZapLogger{zap: logger}, nil
}

// Convert custom Field to zap.Field
func convertFields(fields []Field) []zap.Field {
	zapFields := make([]zap.Field, len(fields))
	for i, f := range fields {
		switch v := f.Value.(type) {
		case string:
			zapFields[i] = zap.String(f.Key, v)
		case int:
			zapFields[i] = zap.Int(f.Key, v)
		case int64:
			zapFields[i] = zap.Int64(f.Key, v)
		case uint64:
			zapFields[i] = zap.Uint64(f.Key, v)
		case float64:
			zapFields[i] = zap.Float64(f.Key, v)
		case bool:
			zapFields[i] = zap.Bool(f.Key, v)
		case time.Duration:
			zapFields[i] = zap.Duration(f.Key, v)
		case error:
			zapFields[i] = zap.Error(v)
		default:
			zapFields[i] = zap.Any(f.Key, v)
		}
	}
	return zapFields
}

func (l *ZapLogger) Debug(msg string, fields ...Field) {
	l.zap.Debug(msg, convertFields(fields)...)
}

func (l *ZapLogger) Info(msg string, fields ...Field) {
	l.zap.Info(msg, convertFields(fields)...)
}

func (l *ZapLogger) Warn(msg string, fields ...Field) {
	l.zap.Warn(msg, convertFields(fields)...)
}

func (l *ZapLogger) Error(msg string, fields ...Field) {
	l.zap.Error(msg, convertFields(fields)...)
}

func (l *ZapLogger) Fatal(msg string, fields ...Field) {
	l.zap.Fatal(msg, convertFields(fields)...)
}

func (l *ZapLogger) With(fields ...Field) Logger {
	return &ZapLogger{
		zap: l.zap.With(convertFields(fields)...),
	}
}

func (l *ZapLogger) Sync() error {
	return l.zap.Sync()
}
