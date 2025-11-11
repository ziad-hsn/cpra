package logger

import (
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapLogger wraps zap.Logger to implement our Logger interface
type ZapLogger struct {
	zap *zap.Logger
}

// NewZapLogger creates a production-ready zap logger with sampling
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
		level = zapcore.InfoLevel
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
		zapConfig.Sampling = &zap.SamplingConfig{
			Initial:    cfg.SampleInitial,
			Thereafter: cfg.SampleThereafter,
		}
	} else {
		zapConfig.Sampling = nil
	}

	// Build logger
	logger, err := zapConfig.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
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
