package secureconfig

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteNewProtectedFile publishes caller-owned bytes outside the state directory
// using the same native ownership, ACL and alias policy as protected key files.
// It never replaces an existing file, adjusts existing permissions, generates a
// key, or records contents. The caller remains responsible for clearing data.
//
// The complete private temporary file is flushed before exclusive publication.
// A post-publication failure may leave the requested file in place; callers must
// inspect it rather than delete it or retry with different secret material.
func WriteNewProtectedFile(ctx context.Context, opts ProtectedFileOptions, data []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > 1<<20 || len(data) == 0 || int64(len(data)) > opts.MaxBytes {
		return fmt.Errorf("%w: protected output must contain 1..1048576 bytes within its declared limit", ErrKeyFile)
	}
	policy, err := validateKeyOptions(KeyFileOptions{Path: opts.Path, DataDirectory: opts.DataDirectory, ReaderGroupID: opts.ReaderGroupID, ReaderSID: opts.ReaderSID})
	if err != nil {
		return err
	}
	resolved, err := resolveKeyPath(policy, true)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(resolved); err == nil {
		return keyFileError("protected output already exists", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return keyFileError("inspect protected output", err)
	}
	parent := filepath.Dir(resolved)
	if err := prepareKeyDirectory(parent, policy); err != nil {
		return err
	}
	if err := checkKeyDirectories(policy, resolved); err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return keyFileError("open protected output directory", err)
	}
	defer root.Close()
	temporary := ".cpra-protected-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return keyFileError("create private protected output", err)
	}
	defer file.Close()
	defer root.Remove(temporary)
	if err := protectNewKey(file, filepath.Join(parent, temporary), policy); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return keyFileError("write protected output", err)
	}
	if err := file.Sync(); err != nil {
		return keyFileError("flush protected output", err)
	}
	if err := checkProtectedHandle(file, policy); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return keyFileError("close protected output", err)
	}
	if err := verifyKeyDirectory(root, parent); err != nil {
		return err
	}
	latest, err := resolveKeyPath(policy, true)
	if err != nil {
		return err
	}
	if latest != resolved {
		return ErrKeyFileChanged
	}
	if err := checkKeyDirectories(policy, resolved); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := publishKeyFile(root, temporary, filepath.Base(resolved)); err != nil {
		return keyFileError("publish protected output", err)
	}
	return nil
}
