//go:build !windows

package secureconfig

import (
	"os"
	"syscall"
)

// CheckPrivateDirectory verifies an opened directory's current-user ownership,
// owner-only mode and native ACL policy without changing its permissions.
func CheckPrivateDirectory(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrKeyFile
	}
	if err := checkUnixOwnerMode(info, KeyFileOptions{}, true); err != nil {
		return err
	}
	return checkKeyAccessACL(file, "")
}

// CheckPrivateFile verifies a single-link, owner-only regular file and its ACL.
func CheckPrivateFile(file *os.File) error {
	return checkProtectedHandle(file, KeyFileOptions{})
}

// CheckTrustedDirectory verifies an opened ancestor without following another
// pathname. A sticky directory cannot replace trusted-owned children.
func CheckTrustedDirectory(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return ErrKeyFile
	}
	if native.Uid != 0 && native.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return ErrKeyFile
	}
	return checkKeyAccessACL(file, "")
}
