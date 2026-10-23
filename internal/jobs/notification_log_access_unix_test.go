//go:build !windows

package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotificationLogDirectoryAccessDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	log, err := OpenNotificationLog(path)
	if log != nil || !errors.Is(err, errNotificationLogDirectoryAccess) || !strings.Contains(err.Error(), "0700") || strings.Contains(err.Error(), path) {
		t.Fatal("private root rejection lost safe remediation", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("opening changed supplied root", err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	log, err = OpenNotificationLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
}
