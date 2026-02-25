// Package logger provides legacy aliases for pkg/log.
// This file will be removed after import paths are updated.
package logger

import (
	"cpra/pkg/log"
)

// Re-export types and interfaces from pkg/log
type (
	Logger        = log.Logger
	Field         = log.Field
	SugaredLogger = log.SugaredLogger
	LoggerConfig  = log.LoggerConfig
	ZapLogger     = log.ZapLogger
)

// Re-export functions from pkg/log
var (
	DefaultConfig                          = log.DefaultConfig
	DevelopmentConfig                      = log.DevelopmentConfig
	NewZapLogger                          = log.NewZapLogger
	NewSugaredLogger                      = log.NewSugaredLogger
	NewSugaredLoggerWithComponentFromConfig = log.NewSugaredLoggerWithComponentFromConfig
	NewLoggerFromConfig                   = log.NewLoggerFromConfig
	NewLoggerWithComponentFromConfig      = log.NewLoggerWithComponentFromConfig
	NewSugaredLoggerFromConfig            = log.NewSugaredLoggerFromConfig
)