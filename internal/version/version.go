// Package version holds build-time version metadata injected by the
// Makefile via -ldflags "-X cpra/internal/version.Version=...". The defaults
// below apply to plain "go build"/"go run" invocations that bypass the
// Makefile, so binaries are always identifiable.
package version

import "fmt"

// These are overridden at build time by the Makefile's LDFLAGS.
var (
	// Version is the semantic version or git describe string (e.g. "v1.2.3"
	// or "abe5ac6-dirty").
	Version = "dev"
	// Commit is the short git commit hash the binary was built from.
	Commit = "unknown"
	// Date is the UTC build timestamp (RFC 3339).
	Date = "unknown"
)

// Info returns a single-line human-readable version string.
func Info() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
