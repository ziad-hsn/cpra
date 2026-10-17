//go:build windows

package jobs

import (
	"errors"
	"os"
	"strings"
)

func openCheckCertificate(path string) (*os.File, error) {
	// Named pipes and device namespaces are not certificate files.
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	if strings.HasPrefix(normalized, `\\.\`) || strings.HasPrefix(normalized, `\\?\`) {
		return nil, errors.New("TLS CA file must be a regular file")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("TLS CA file must be a regular file")
	}
	return os.Open(path)
}
