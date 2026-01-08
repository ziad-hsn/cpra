// Package mtls provides legacy aliases for pkg/health (mtls functionality moved there).
// This file will be removed after import paths are updated.
package mtls

import (
	"cpra/pkg/health"
)

// Re-export types from pkg/health (mtls moved there)
type CertificateManager = health.CertificateManager

// Re-export functions from pkg/health
var NewCertificateManager = health.NewCertificateManager