// Package observability provides legacy aliases for pkg/observability.
// This file will be removed after import paths are updated.
package observability

import (
	"cpra/pkg/observability"
)

// Re-export types from pkg/observability
type (
	DatabaseInstrumentationConfig = observability.DatabaseInstrumentationConfig
	InstrumentedDB               = observability.InstrumentedDB
)

// Re-export functions from pkg/observability
var (
	SetupPprofServer  = observability.SetupPprofServer
	StopPprofServer   = observability.StopPprofServer
	NewInstrumentedDB = observability.NewInstrumentedDB
	InitializeGlobal  = observability.InitializeGlobal
)