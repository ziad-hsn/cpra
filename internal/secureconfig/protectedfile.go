package secureconfig

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ProtectedFileOptions applies the same native ownership, ACL, alias and
// external-to-state policy as KeyFileOptions to a bounded bootstrap source.
// MaxBytes must be between 1 and 1 MiB. No file or directory is ever created.
type ProtectedFileOptions struct {
	Path          string
	DataDirectory string
	ReaderGroupID *int
	ReaderSID     string
	MaxBytes      int64
}

// ReadProtectedFile returns caller-owned source bytes. Callers should clear the
// slice when done and must not log it. All path components and the actual opened
// regular file receive the same strict native protection checks as local keys.
// This also suits public CA certificates when trust integrity requires the same
// owner/service-reader policy. Exact-32-byte key loading remains a separate API.
func ReadProtectedFile(ctx context.Context, opts ProtectedFileOptions) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > 1<<20 {
		return nil, fmt.Errorf("%w: protected source limit must be 1..1048576 bytes", ErrKeyFile)
	}
	policy, err := validateKeyOptions(KeyFileOptions{Path: opts.Path, DataDirectory: opts.DataDirectory, ReaderGroupID: opts.ReaderGroupID, ReaderSID: opts.ReaderSID})
	if err != nil {
		return nil, err
	}
	resolved, err := resolveKeyPath(policy, false)
	if err != nil {
		return nil, err
	}
	if err := checkKeyDirectories(policy, resolved); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, keyFileError("open protected source directory", err)
	}
	defer root.Close()
	file, err := openKeyFile(root, filepath.Base(resolved))
	if err != nil {
		return nil, keyFileError("open protected source", err)
	}
	defer file.Close()
	if err := checkProtectedHandle(file, policy); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, keyFileError("inspect protected source", err)
	}
	if info.Size() > opts.MaxBytes {
		return nil, fmt.Errorf("%w: protected source exceeds configured limit", ErrKeyFile)
	}
	data, err := io.ReadAll(io.LimitReader(file, opts.MaxBytes+1))
	if err != nil {
		clear(data)
		return nil, keyFileError("read protected source", err)
	}
	if int64(len(data)) > opts.MaxBytes {
		clear(data)
		return nil, fmt.Errorf("%w: protected source exceeds configured limit", ErrKeyFile)
	}
	if err := verifyProtectedAccess(file, root, policy, resolved); err != nil {
		clear(data)
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}
