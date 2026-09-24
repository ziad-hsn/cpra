//go:build !windows

package secureconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func checkKeyPlatformOptions(opts KeyFileOptions) error {
	if opts.ReaderSID != "" || (opts.ReaderGroupID != nil && (*opts.ReaderGroupID < 0 || uint64(*opts.ReaderGroupID) >= uint64(^uint32(0)))) {
		return fmt.Errorf("%w: invalid Unix reader policy", ErrKeyFile)
	}
	return nil
}

func keyPathWithin(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func prepareKeyDirectory(path string, opts KeyFileOptions) error {
	// Inspect ancestors before creating even an empty directory. Existing paths
	// are never chowned or chmodded by provisioning.
	if err := checkUnixAncestors(filepath.Dir(path), opts); err != nil {
		return err
	}
	if opts.ReaderGroupID != nil && os.Geteuid() != 0 {
		return fmt.Errorf("%w: shared key provisioning requires root", ErrKeyFile)
	}
	err := os.Mkdir(path, 0700)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return keyFileError("create key directory", err)
	}
	if opts.ReaderGroupID != nil {
		if err := os.Chown(path, 0, *opts.ReaderGroupID); err != nil {
			return keyFileError("set key directory group", err)
		}
		if err := os.Chmod(path, 0750); err != nil {
			return keyFileError("set key directory permissions", err)
		}
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return keyFileError("open new key directory parent", err)
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return keyFileError("flush new key directory entry", err)
	}
	return nil
}

func openKeyFile(root *os.Root, name string) (*os.File, error) {
	// Refuse a raced-in symlink and avoid blocking on a raced-in FIFO/device.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func protectNewKey(file *os.File, _ string, opts KeyFileOptions) error {
	if opts.ReaderGroupID == nil {
		return checkKeyAccessACL(file, "")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: shared key provisioning requires root", ErrKeyFile)
	}
	if err := file.Chown(0, *opts.ReaderGroupID); err != nil {
		return keyFileError("set key group", err)
	}
	if err := file.Chmod(0640); err != nil {
		return keyFileError("set key permissions", err)
	}
	return checkKeyAccessACL(file, "")
}

func checkKeyHandle(file *os.File, opts KeyFileOptions) error {
	if err := checkProtectedHandle(file, opts); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return keyFileError("inspect key", err)
	}
	if info.Size() != keySize {
		return fmt.Errorf("%w: key must be a single-link regular 32-byte file", ErrKeyFile)
	}
	return nil
}

func checkProtectedHandle(file *os.File, opts KeyFileOptions) error {
	info, err := file.Stat()
	if err != nil {
		return keyFileError("inspect protected file", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
		return fmt.Errorf("%w: source must be a single-link regular file", ErrKeyFile)
	}
	if err := checkUnixOwnerMode(info, opts, false); err != nil {
		return err
	}
	return checkKeyAccessACL(file, "")
}

func checkUnixOwnerMode(info os.FileInfo, opts KeyFileOptions, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: ownership unavailable", ErrKeyFile)
	}
	owner := uint32(os.Geteuid())
	allowed, required := os.FileMode(0600), os.FileMode(0400)
	if directory {
		allowed, required = 0700, 0500
	}
	if opts.ReaderGroupID != nil {
		owner = 0
		if uint64(stat.Gid) != uint64(*opts.ReaderGroupID) {
			return fmt.Errorf("%w: unexpected reader group", ErrKeyFile)
		}
		allowed, required = 0640, 0440
		if directory {
			allowed, required = 0750, 0550
		}
	}
	if stat.Uid != owner || info.Mode().Perm() & ^allowed != 0 || info.Mode().Perm()&required != required || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("%w: key ownership or permissions are too broad or unreadable", ErrKeyFile)
	}
	return nil
}

func checkKeyDirectories(opts KeyFileOptions, resolved string) error {
	info, err := os.Stat(filepath.Dir(resolved))
	if err != nil {
		return keyFileError("inspect key directory", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: key directory is not a directory", ErrKeyFile)
	}
	if err := checkUnixOwnerMode(info, opts, true); err != nil {
		return err
	}
	if err := checkKeyAccessACL(nil, filepath.Dir(resolved)); err != nil {
		return err
	}
	if err := checkUnixAncestors(filepath.Dir(resolved), opts); err != nil {
		return err
	}
	// Inspect the supplied alias as well as its canonical destination. Otherwise
	// an untrusted writer could redirect a projected Secret's symlink chain.
	if err := checkUnixAncestors(filepath.Dir(opts.Path), opts); err != nil {
		return err
	}
	// Generation has no final file yet and has already rejected aliases.
	if _, err := os.Lstat(opts.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	directories, err := aliasDirectories(opts.Path)
	if err != nil {
		return err
	}
	for _, dir := range directories {
		if err := checkUnixAncestors(dir, opts); err != nil {
			return err
		}
	}
	return nil
}

func checkUnixAncestors(path string, opts KeyFileOptions) error {
	for {
		info, err := os.Stat(path)
		if err != nil {
			return keyFileError("inspect key path permissions", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() {
			return fmt.Errorf("%w: invalid key path directory", ErrKeyFile)
		}
		trusted := stat.Uid == 0 || (opts.ReaderGroupID == nil && stat.Uid == uint32(os.Geteuid()))
		// A sticky directory prevents other users from replacing trusted-owned
		// children. The next component is checked for ownership independently.
		if !trusted || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("%w: key path is writable by an untrusted principal", ErrKeyFile)
		}
		if err := checkKeyAccessACL(nil, path); err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
