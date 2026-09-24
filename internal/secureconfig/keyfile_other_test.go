//go:build !linux && !windows

package secureconfig

import "testing"

func keyFileTestDirectory(t *testing.T) string { t.Helper(); return t.TempDir() }
