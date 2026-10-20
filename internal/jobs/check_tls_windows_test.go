//go:build windows

package jobs

import (
	"path/filepath"
	"testing"
)

func TestCheckPreparationWindowsFileCauses(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ path, code string }{
		{filepath.Join(dir, "absent.pem"), "ca_file_missing"},
		{dir, "ca_file_invalid"},
		{`\\.\pipe\cpra-ca-test`, "ca_file_invalid"},
		{`\\?\GLOBALROOT\Device\cpra-ca-test`, "ca_file_invalid"},
	} {
		_, err := checkTLSConfiguration(tc.path, "localhost", false)
		if code := ConfigurationFailure(err); code != tc.code {
			t.Fatal("incorrect local preparation cause", code, err)
		}
	}
}
