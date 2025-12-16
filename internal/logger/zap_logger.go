package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapLogger wraps zap.SugaredLogger to implement our Logger interface.
// SugaredLogger supports both printf-style (Infof) and loose key-value (Infow) logging.
type ZapLogger struct {
	sugar *zap.SugaredLogger
	base  *zap.Logger
}

// buildZapConfig creates a zap.Config from LoggerConfig
func buildZapConfig(cfg LoggerConfig) zap.Config {
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

	return zapConfig
}

// NewZapLogger creates a production-ready zap logger with sampling
// that implements the Logger interface for structured logging.
func NewZapLogger(cfg LoggerConfig) (*ZapLogger, error) {
	zapConfig := buildZapConfig(cfg)

	// Build logger
	logger, err := zapConfig.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return nil, err
	}

	return &ZapLogger{
		base:  logger,
		sugar: logger.Sugar(),
	}, nil
}

// NewSugaredLogger creates a zap SugaredLogger for printf-style logging.
// Use methods like Infof, Debugf, Warnf, Errorf, Fatalf for formatted output.
// Use methods like Infow, Debugw, etc. for loose key-value structured logging.
//
// Example:
//
//	sugar, _ := NewSugaredLogger(DefaultConfig())
//	defer sugar.Sync()
//	sugar.Infof("Server started on port %d", 8080)
//	sugar.Infow("Request completed", "method", "GET", "path", "/api", "duration", 42)
func NewSugaredLogger(cfg LoggerConfig) (*zap.SugaredLogger, error) {
	zapConfig := buildZapConfig(cfg)

	logger, err := zapConfig.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return nil, err
	}

	return logger.Sugar(), nil
}

// NewSugaredLoggerWithComponent creates a SugaredLogger with a component field pre-set.
// This is useful for creating component-specific loggers that automatically include
// the component name in all log entries.
//
// Example:
//
//	systemLog, _ := NewSugaredLoggerWithComponent("SYSTEM", DefaultConfig())
//	systemLog.Infof("System initialized") // logs with component=SYSTEM
func NewSugaredLoggerWithComponent(component string, cfg LoggerConfig) (*zap.SugaredLogger, error) {
	sugar, err := NewSugaredLogger(cfg)
	if err != nil {
		return nil, err
	}
	return sugar.With("component", component), nil
}

// fieldsToArgs converts Field slice to alternating key-value args for SugaredLogger
func fieldsToArgs(fields []Field) []interface{} {
	args := make([]interface{}, 0, len(fields)*2)
	for _, f := range fields {
		args = append(args, f.Key, f.Value)
	}
	return args
}

func (l *ZapLogger) Debug(msg string, fields ...Field) {
	if len(fields) > 0 {
		l.sugar.Debugw(msg, fieldsToArgs(fields)...)
	} else {
		l.sugar.Debug(msg)
	}
}

func (l *ZapLogger) Info(msg string, fields ...Field) {
	if len(fields) > 0 {
		l.sugar.Infow(msg, fieldsToArgs(fields)...)
	} else {
		l.sugar.Info(msg)
	}
}

func (l *ZapLogger) Warn(msg string, fields ...Field) {
	if len(fields) > 0 {
		l.sugar.Warnw(msg, fieldsToArgs(fields)...)
	} else {
		l.sugar.Warn(msg)
	}
}

func (l *ZapLogger) Error(msg string, fields ...Field) {
	if len(fields) > 0 {
		l.sugar.Errorw(msg, fieldsToArgs(fields)...)
	} else {
		l.sugar.Error(msg)
	}
}

func (l *ZapLogger) Fatal(msg string, fields ...Field) {
	if len(fields) > 0 {
		l.sugar.Fatalw(msg, fieldsToArgs(fields)...)
	} else {
		l.sugar.Fatal(msg)
	}
}

func (l *ZapLogger) With(fields ...Field) Logger {
	return &ZapLogger{
		base:  l.base,
		sugar: l.sugar.With(fieldsToArgs(fields)...),
	}
}

func (l *ZapLogger) Sync() error {
	return l.base.Sync()
}
