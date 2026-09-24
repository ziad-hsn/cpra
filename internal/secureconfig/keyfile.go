package secureconfig

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrKeyFile        = errors.New("secureconfig: unsafe or invalid local key file")
	ErrKeyFileChanged = errors.New("secureconfig: local key path changed during access")
)

// KeyFileOptions declares a local wrapping key and its permitted readers. Path
// and DataDirectory must be absolute. Keys must be outside the entire state
// directory, including symlink aliases, so ordinary state backups exclude them.
//
// The file contains exactly 32 random bytes, without text encoding or a newline.
// On Unix, the default is an owner-only file in a private directory. An explicit
// ReaderGroupID permits a root-owned file and directory with read/search access
// for that group only. On Windows, protected DACLs permit the owner (user mode),
// SYSTEM and Administrators; an explicit ReaderSID selects system mode and grants
// that service SID read access only. Options for the other OS are rejected.
// Linux extended access ACLs are rejected. Other Unix systems currently fail
// closed until their native ACL verifier is implemented and qualified.
type KeyFileOptions struct {
	Path          string
	DataDirectory string
	ReaderGroupID *int
	ReaderSID     string
}

// LoadLocalKeyFile opens an existing key without creating or repairing anything.
// External projected Secret symlinks are allowed when the resolved file and all
// path components meet the access policy. Both the opened file and the path are
// checked again after reading. A missing key remains an error, never a new key.
func LoadLocalKeyFile(ctx context.Context, opts KeyFileOptions) (*LocalWrapper, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	opts, err := validateKeyOptions(opts)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveKeyPath(opts, false)
	if err != nil {
		return nil, err
	}
	if err := checkKeyDirectories(opts, resolved); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, keyFileError("open key directory", err)
	}
	defer root.Close()
	file, err := openKeyFile(root, filepath.Base(resolved))
	if err != nil {
		return nil, keyFileError("open key", err)
	}
	defer file.Close()
	if err := checkKeyHandle(file, opts); err != nil {
		return nil, err
	}
	var key [keySize + 1]byte
	defer clear(key[:])
	n, err := io.ReadFull(file, key[:])
	if n != keySize || !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("%w: key must contain exactly 32 bytes", ErrInvalidKey)
	}
	if err := verifyKeyAccess(file, root, opts, resolved); err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return NewLocalWrapper(key[:keySize])
}

// GenerateLocalKeyFile explicitly provisions a new key and returns only its
// non-secret identity. It does not activate configuration, replace a key or
// change existing permissions. The immediate key directory may be created;
// its parent must already exist and satisfy the ownership policy.
//
// Publication is exclusive and occurs only after the complete private temporary
// file has been flushed. A failure after publication may leave the new key in
// place; callers must inspect/reuse it, never delete it and generate a replacement.
// Cancellation before publication cleans up the temporary file. Once published,
// the remaining durability operations finish without consulting cancellation.
func GenerateLocalKeyFile(ctx context.Context, opts KeyFileOptions) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	opts, err := validateKeyOptions(opts)
	if err != nil {
		return "", err
	}
	resolved, err := resolveKeyPath(opts, true)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(resolved); err == nil {
		return "", keyFileError("key already exists", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", keyFileError("inspect key", err)
	}
	parent := filepath.Dir(resolved)
	if err := prepareKeyDirectory(parent, opts); err != nil {
		return "", err
	}
	if err := checkKeyDirectories(opts, resolved); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", keyFileError("open key directory", err)
	}
	defer root.Close()
	temporary := ".cpra-key-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", keyFileError("create private temporary key", err)
	}
	defer file.Close()
	defer root.Remove(temporary)
	if err := protectNewKey(file, filepath.Join(parent, temporary), opts); err != nil {
		return "", err
	}
	var key [keySize]byte
	defer clear(key[:])
	if _, err := rand.Read(key[:]); err != nil {
		return "", keyFileError("generate key", err)
	}
	wrapper, err := NewLocalWrapper(key[:])
	if err != nil {
		return "", err
	}
	if _, err := file.Write(key[:]); err != nil {
		return "", keyFileError("write key", err)
	}
	if err := file.Sync(); err != nil {
		return "", keyFileError("flush key", err)
	}
	if err := checkKeyHandle(file, opts); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", keyFileError("close key", err)
	}
	if err := verifyKeyDirectory(root, parent); err != nil {
		return "", err
	}
	latest, err := resolveKeyPath(opts, true)
	if err != nil {
		return "", err
	}
	if latest != resolved {
		return "", ErrKeyFileChanged
	}
	if err := checkKeyDirectories(opts, resolved); err != nil {
		return "", err
	}
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if err := publishKeyFile(root, temporary, filepath.Base(resolved)); err != nil {
		return "", keyFileError("publish key", err)
	}
	return wrapper.ID(), nil
}

func validateKeyOptions(opts KeyFileOptions) (KeyFileOptions, error) {
	if !filepath.IsAbs(opts.Path) || !filepath.IsAbs(opts.DataDirectory) || strings.IndexByte(opts.Path, 0) >= 0 || strings.IndexByte(opts.DataDirectory, 0) >= 0 {
		return opts, fmt.Errorf("%w: key and state paths must be absolute", ErrKeyFile)
	}
	opts.Path, opts.DataDirectory = filepath.Clean(opts.Path), filepath.Clean(opts.DataDirectory)
	if opts.ReaderGroupID != nil {
		group := *opts.ReaderGroupID
		opts.ReaderGroupID = &group
	}
	return opts, checkKeyPlatformOptions(opts)
}

func resolveKeyPath(opts KeyFileOptions, creating bool) (string, error) {
	state, err := resolveExistingPrefix(opts.DataDirectory)
	if err != nil {
		return "", err
	}
	var path string
	if creating {
		if err := rejectKeySymlinks(opts.Path); err != nil {
			return "", err
		}
		path, err = resolveExistingPrefix(opts.Path)
	} else {
		path, err = filepath.EvalSymlinks(opts.Path)
		if err != nil {
			err = keyFileError("resolve key", err)
		}
	}
	if err != nil {
		return "", err
	}
	if keyPathWithin(path, state) || keyPathWithin(opts.Path, opts.DataDirectory) {
		return "", fmt.Errorf("%w: key must be outside the state directory", ErrKeyFile)
	}
	if err := checkStateDirectoryIdentity(path, state); err != nil {
		return "", err
	}
	return path, nil
}

func checkStateDirectoryIdentity(path, state string) error {
	stateInfo, err := os.Stat(state)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return keyFileError("inspect state directory identity", err)
	}
	// EvalSymlinks does not resolve bind mounts. Compare ancestor identities as
	// well so an existing state directory mounted under a second name cannot
	// cause a key to enter the ordinary complete-directory backup.
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return keyFileError("inspect key directory identity", err)
		}
		if err == nil && os.SameFile(info, stateInfo) {
			return fmt.Errorf("%w: key directory aliases the state directory", ErrKeyFile)
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

func resolveExistingPrefix(path string) (string, error) {
	var missing []string
	for {
		_, err := os.Lstat(path)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", keyFileError("resolve path", err)
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", keyFileError("inspect path", err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", keyFileError("resolve path root", err)
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func rejectKeySymlinks(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return keyFileError("inspect key path", err)
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: generation refuses symlink paths", ErrKeyFile)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func verifyKeyAccess(file *os.File, root *os.Root, opts KeyFileOptions, resolved string) error {
	if err := verifyProtectedAccess(file, root, opts, resolved); err != nil {
		return err
	}
	return checkKeyHandle(file, opts)
}

func verifyProtectedAccess(file *os.File, root *os.Root, opts KeyFileOptions, resolved string) error {
	latest, err := resolveKeyPath(opts, false)
	if err != nil {
		return err
	}
	if latest != resolved {
		return ErrKeyFileChanged
	}
	if err := verifyKeyDirectory(root, filepath.Dir(resolved)); err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		return keyFileError("inspect opened key", err)
	}
	current, err := root.Lstat(filepath.Base(resolved))
	if err != nil {
		return keyFileError("inspect key path", err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return ErrKeyFileChanged
	}
	if err := checkProtectedHandle(file, opts); err != nil {
		return err
	}
	return checkKeyDirectories(opts, resolved)
}

func verifyKeyDirectory(root *os.Root, path string) error {
	opened, err := root.Stat(".")
	if err != nil {
		return keyFileError("inspect opened key directory", err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return keyFileError("inspect key directory", err)
	}
	if !current.IsDir() || !os.SameFile(opened, current) {
		return ErrKeyFileChanged
	}
	return nil
}

// aliasDirectories enumerates each symlink's containing directory, including
// intermediate aliases which EvalSymlinks hides in its final resolved path.
func aliasDirectories(path string) ([]string, error) {
	var directories []string
	for links := 0; ; links++ {
		if links > 40 {
			return nil, fmt.Errorf("%w: too many key path aliases", ErrKeyFile)
		}
		volume := filepath.VolumeName(path)
		parts := strings.Split(strings.TrimPrefix(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
		prefix, followed := volume+string(filepath.Separator), false
		for i, part := range parts {
			prefix = filepath.Join(prefix, part)
			info, err := os.Lstat(prefix)
			if err != nil {
				return nil, keyFileError("inspect key alias", err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			directories = append(directories, filepath.Dir(prefix))
			target, err := os.Readlink(prefix)
			if err != nil {
				return nil, keyFileError("resolve key alias", err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(prefix), target)
			}
			path = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			followed = true
			break
		}
		if !followed {
			return directories, nil
		}
	}
}

// Filesystem errors retain their sentinel/code for callers without exposing path
// text or native diagnostics. Key contents never enter an error value.
func keyFileError(operation string, err error) error {
	var path *os.PathError
	if errors.As(err, &path) {
		err = path.Err
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		err = link.Err
	}
	return fmt.Errorf("%w: %s: %w", ErrKeyFile, operation, err)
}
