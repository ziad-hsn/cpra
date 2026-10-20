//go:build windows

package jobs

import (
	"os"
	"strings"
)

func openCheckCertificate(path string) (*os.File, error) {
	// Named pipes and device namespaces are not certificate files.
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	if strings.HasPrefix(normalized, `\\.\`) || strings.HasPrefix(normalized, `\\?\`) {
		return nil, errCertificateFileType
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errCertificateFileType
	}
	return os.Open(path)
}
