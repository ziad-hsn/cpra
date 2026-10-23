//go:build !windows

package localadmin

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func prepareConfigDirectory(installpath.Layout, string) error { return nil }

func writeInitialConfig(l installpath.Layout, account, name string, data []byte) error {
	if name != "auth.token" {
		return writeNew(filepath.Join(l.ConfigDir, name), data, 0600)
	}
	group, _, err := managementServiceReader(l, account)
	if err != nil {
		return err
	}
	return secureconfig.WriteNewProtectedFile(context.Background(), secureconfig.ProtectedFileOptions{
		Path: filepath.Join(l.ConfigDir, name), DataDirectory: l.StateDir, ReaderGroupID: group, MaxBytes: 4096,
	}, data)
}

// Existing bearer files are supplied credentials, even at the conventional
// installer path. Check them before repairing any managed directory; permissions
// must not turn an exposed or differently owned token into a trusted source.
func checkExistingToken(l installpath.Layout, account string) error {
	path, err := filepath.Abs(filepath.Join(l.ConfigDir, "auth.token"))
	if err != nil {
		return errors.New("existing API token location cannot be resolved safely")
	}
	for _, managed := range []string{l.StateDir, l.LogDir} {
		if managed == "" {
			continue
		}
		absolute, err := filepath.Abs(managed)
		if err != nil {
			return errors.New("existing API token location cannot be resolved safely")
		}
		if insidePath(path, absolute) {
			return errors.New("existing API token location overlaps writable state or logs; use separate protected configuration")
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("existing API token cannot be inspected safely; preserve it and use a protected replacement")
	}
	group, _, err := managementServiceReader(l, account)
	if err != nil {
		return err
	}
	data, err := secureconfig.ReadTokenFile(context.Background(), secureconfig.TokenFileOptions{
		Path: path, Root: l.ConfigDir, ReaderGroupID: group, MaxBytes: 4096,
	})
	clear(data)
	if err != nil {
		return &authenticationError{"existing API token protection is unsafe or unavailable; its ownership and permissions were not changed", err}
	}
	return nil
}
