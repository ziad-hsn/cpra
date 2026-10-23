package secureconfig

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	ErrTokenFilePolicy = errors.New("API token file ownership, permissions or trusted root are unsafe")
	ErrTokenFileSize   = errors.New("API token file exceeds its byte limit")
	ErrTokenFileType   = errors.New("API token source must be a single-link regular file")
	ErrTokenFileChange = errors.New("API token file changed while being read; retry the local read")
)

// TokenFileOptions selects a bounded credential source without changing it.
// Root defaults to the file's directory. File aliases require an explicit Root;
// Kubernetes ..data projections may resolve within it but may never escape it. Unix files
// are owner-only unless ReaderGroupID explicitly trusts a root-owned group-read
// source. ReaderSID selects the existing Windows service-reader DACL policy.
// This policy is deliberately separate from encryption keys' state exclusion.
type TokenFileOptions struct {
	Path          string
	Root          string
	ReaderGroupID *int
	ReaderSID     string
	MaxBytes      int64
}

// TokenReaderGroup resolves an explicit numeric or named Unix reader identity. An empty
// value selects owner-only access; invalid input is never echoed into errors.
func TokenReaderGroup(value string) (*int, error) {
	if value == "" {
		return nil, nil
	}
	group, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		if len(value) > 128 || strings.ContainsAny(value, "/\\\x00\r\n\t ") {
			return nil, ErrTokenFilePolicy
		}
		entry, lookupErr := user.LookupGroup(value)
		if lookupErr != nil {
			return nil, ErrTokenFilePolicy
		}
		group, err = strconv.ParseUint(entry.Gid, 10, 32)
	}
	if err != nil || group == 1<<32-1 || uint64(int(group)) != group {
		return nil, ErrTokenFilePolicy
	}
	result := int(group)
	return &result, nil
}

// ReadTokenFile checks native policy on the actual opened handle before and
// after a bounded read. It returns caller-owned bytes to clear after use. Errors
// contain no path, file contents, native ACL text or token value.
func ReadTokenFile(ctx context.Context, opts TokenFileOptions) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if opts.Path == "" || opts.MaxBytes < 1 || opts.MaxBytes > 1<<20 {
		return nil, ErrTokenFilePolicy
	}
	path, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, ErrTokenFilePolicy
	}
	base := opts.Root
	if base == "" {
		base = filepath.Dir(path)
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, ErrTokenFilePolicy
	}
	name, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(name) || name == "." {
		return nil, ErrTokenFilePolicy
	}
	policy := KeyFileOptions{Path: path, DataDirectory: base, ReaderGroupID: opts.ReaderGroupID, ReaderSID: opts.ReaderSID}
	if err := checkKeyPlatformOptions(policy); err != nil {
		return nil, ErrTokenFilePolicy
	}
	if err := checkTokenReader(policy); err != nil {
		return nil, ErrTokenFilePolicy
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, tokenFileError(err)
	}
	if err := checkTokenAncestors(base, resolved, policy); err != nil {
		return nil, tokenFileError(err)
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, tokenFileError(err)
	}
	defer root.Close()
	if err := verifyKeyDirectory(root, resolved); err != nil {
		return nil, ErrTokenFileChange
	}
	if err := checkTokenRoot(root, policy); err != nil {
		return nil, ErrTokenFilePolicy
	}
	originalName := name
	name, err = resolveTokenName(root, name, policy)
	if err != nil {
		return nil, err
	}
	if opts.Root == "" && name != originalName {
		return nil, ErrTokenFilePolicy
	}
	file, err := openKeyFile(root, name)
	if err != nil {
		return nil, tokenFileError(err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, tokenFileError(err)
	}
	if !before.Mode().IsRegular() {
		return nil, ErrTokenFileType
	}
	if err := checkProtectedHandle(file, policy); err != nil {
		return nil, ErrTokenFilePolicy
	}
	if before.Size() > opts.MaxBytes {
		return nil, ErrTokenFileSize
	}
	data, err := io.ReadAll(io.LimitReader(file, opts.MaxBytes+1))
	if err != nil {
		clear(data)
		return nil, tokenFileError(err)
	}
	fail := func(err error) ([]byte, error) { clear(data); return nil, err }
	if int64(len(data)) > opts.MaxBytes {
		return fail(ErrTokenFileSize)
	}
	after, err := file.Stat()
	if err != nil {
		return fail(tokenFileError(err))
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(after, current) || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != after.Size() {
		return fail(ErrTokenFileChange)
	}
	if err := checkProtectedHandle(file, policy); err != nil {
		return fail(ErrTokenFilePolicy)
	}
	if _, err := resolveTokenName(root, name, policy); err != nil {
		return fail(err)
	}
	latest, err := filepath.EvalSymlinks(base)
	if err != nil || latest != resolved || verifyKeyDirectory(root, resolved) != nil {
		return fail(ErrTokenFileChange)
	}
	if err := checkTokenAncestors(base, resolved, policy); err != nil {
		return fail(tokenFileError(err))
	}
	if err := checkTokenRoot(root, policy); err != nil {
		return fail(ErrTokenFilePolicy)
	}
	if err := contextError(ctx); err != nil {
		return fail(err)
	}
	return data, nil
}

func checkTokenRoot(root *os.Root, policy KeyFileOptions) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return checkTokenDirectory(directory, policy)
}

// Walk only through the opened root. Every alias is bounded and every directory
// is inspected through its own handle. A rotating ..data link can select the old
// or new protected generation; no HTTP request is replayed by this reader.
func resolveTokenName(root *os.Root, name string, policy KeyFileOptions) (string, error) {
	for links := 0; links <= 40; links++ {
		parts := strings.Split(name, string(filepath.Separator))
		followed := false
		for i := range parts {
			prefix := filepath.Join(parts[:i+1]...)
			info, err := root.Lstat(prefix)
			if err != nil {
				return "", tokenFileError(err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := root.Readlink(prefix)
				if err != nil || filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
					return "", ErrTokenFilePolicy
				}
				name = filepath.Join(append([]string{filepath.Dir(prefix), target}, parts[i+1:]...)...)
				if !filepath.IsLocal(name) || name == "." {
					return "", ErrTokenFilePolicy
				}
				followed = true
				break
			}
			if i < len(parts)-1 {
				directory, err := root.Open(prefix)
				if err != nil {
					return "", tokenFileError(err)
				}
				err = checkTokenDirectory(directory, policy)
				directory.Close()
				if err != nil {
					return "", ErrTokenFilePolicy
				}
			}
		}
		if !followed {
			return name, nil
		}
	}
	return "", ErrTokenFilePolicy
}

func tokenFileError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.ErrNotExist
	case errors.Is(err, os.ErrPermission):
		return os.ErrPermission
	default:
		return ErrTokenFilePolicy
	}
}
