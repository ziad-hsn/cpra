package secureconfig

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func checkKeyAccessACL(file *os.File, path string) error {
	var size int
	var err error
	if file != nil {
		size, err = unix.Fgetxattr(int(file.Fd()), "system.posix_acl_access", nil)
	} else {
		size, err = unix.Getxattr(path, "system.posix_acl_access", nil)
	}
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	if err != nil {
		return keyFileError("inspect extended access permissions", err)
	}
	// Named-user ACL entries can grant read access despite otherwise acceptable
	// 0640 mode bits. This initial policy requires the simpler owner/group model.
	if size != 0 {
		return fmt.Errorf("%w: extended access ACLs are unsupported for local key paths", ErrKeyFile)
	}
	return nil
}
