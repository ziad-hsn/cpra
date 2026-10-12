package persistence

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/go-hclog"
	applogger "github.com/ziad-hsn/cpra/internal/logger"
)

// raftLogSink adapts framework diagnostics without copying arbitrary error
// strings, configuration objects or filesystem paths into application records.
type raftLogSink struct{ logger applogger.Logger }

func newRaftLogger() (hclog.Logger, error) {
	logger, err := applogger.NewLoggerWithComponent("raft")
	if err != nil {
		return nil, err
	}
	framework := hclog.NewInterceptLogger(&hclog.LoggerOptions{Name: "raft", Level: hclog.Debug, Output: io.Discard})
	framework.RegisterSink(&raftLogSink{logger: logger})
	return framework, nil
}

func (s *raftLogSink) Accept(_ string, level hclog.Level, message string, args ...interface{}) {
	fields := make([]applogger.Field, 0, min(len(args)/2, 16))
	for i := 0; i+1 < len(args) && len(fields) < 16; i += 2 {
		key, ok := args[i].(string)
		if !ok || len(key) > 64 || strings.ContainsAny(key, "\r\n\x1b") {
			continue
		}
		key = strings.ReplaceAll(key, "-", "_")
		if key == "error" {
			cause := "storage_error"
			if err, ok := args[i+1].(error); ok {
				switch {
				case errors.Is(err, os.ErrPermission):
					cause = "permission_denied"
				case errors.Is(err, context.DeadlineExceeded):
					cause = "deadline_exceeded"
				case errors.Is(err, context.Canceled):
					cause = "cancelled"
				}
			}
			fields = append(fields, applogger.Field{Key: "error_code", Value: cause})
			continue
		}
		// Counters and flags are useful. Text and complex objects may include
		// paths, addresses or configuration; retain none of them.
		switch value := args[i+1].(type) {
		case bool, int, int32, int64, uint, uint32, uint64, float64:
			fields = append(fields, applogger.Field{Key: key, Value: value})
		}
	}
	switch level {
	case hclog.Trace, hclog.Debug:
		s.logger.Debug(message, fields...)
	case hclog.Warn:
		s.logger.Warn(message, fields...)
	case hclog.Error:
		s.logger.Error(message, fields...)
	default:
		s.logger.Info(message, fields...)
	}
}
