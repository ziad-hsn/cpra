// Package health provides legacy aliases for pkg/health.
// This file will be removed after import paths are updated.
package health

import (
	"cpra/pkg/health"
)

// Re-export types from pkg/health
type (
	Status               = health.Status
	CheckResult          = health.CheckResult
	Checker              = health.Checker
	HealthManager        = health.HealthManager
	CertificateManager   = health.CertificateManager
	TCPChecker          = health.TCPChecker
	HTTPChecker         = health.HTTPChecker
	FuncChecker         = health.FuncChecker
	DatabaseChecker     = health.DatabaseChecker
	CompositeChecker    = health.CompositeChecker
	CompositePolicy     = health.CompositePolicy
)

// Re-export constants from pkg/health
const (
	StatusHealthy   = health.StatusHealthy
	StatusUnhealthy = health.StatusUnhealthy
	StatusUnknown   = health.StatusUnknown
	
	AllMustPass = health.AllMustPass
	AnyCanPass  = health.AnyCanPass
)

// Re-export functions from pkg/health
var (
	NewHealthManager      = health.NewHealthManager
	NewCertificateManager = health.NewCertificateManager
	NewTCPChecker        = health.NewTCPChecker
	NewHTTPChecker       = health.NewHTTPChecker
	NewFuncChecker       = health.NewFuncChecker
	NewDatabaseChecker   = health.NewDatabaseChecker
	NewCompositeChecker  = health.NewCompositeChecker
)