// Package encryptionsetup constructs process-owned encryption dependencies before
// management admission. It never provisions or replaces persistent keys, opens a
// Raft store, or invokes monitor providers. Remote backend initialization does
// contact the explicitly selected wrapping service to verify its key contract.
package encryptionsetup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

var (
	ErrConfiguration      = errors.New("encryptionsetup: invalid encryption configuration")
	ErrEncryptionRequired = errors.New("encryptionsetup: durable management requires an explicitly initialized encryption backend")
	ErrBackend            = errors.New("encryptionsetup: encryption backend initialization failed")
)

// Options contains bootstrap descriptors only. DataDirectory is the resolved,
// absolute state/backup boundary, including when memory uses an explicit source.
// A nil Encryption is valid only with StorageMode exactly "memory".
type Options struct {
	StorageMode   string
	DataDirectory string
	Encryption    *runtimeconfig.ManagementEncryption
}

func (Options) String() string     { return "encryptionsetup.Options{sources:redacted}" }
func (o Options) GoString() string { return o.String() }

// Handle owns the Sealer's wrapping adapters. Close releases owned idle
// transports after management users stop; it does not interrupt in-flight calls
// or promise erasure of Go cipher schedules. It is safe to call concurrently.
type Handle struct {
	sealer     *secureconfig.Sealer
	closeOnce  sync.Once
	closeFuncs []func()
}

func (h *Handle) Sealer() *secureconfig.Sealer {
	if h == nil {
		return nil
	}
	return h.sealer
}
func (h *Handle) Close() error {
	if h != nil {
		h.closeOnce.Do(func() {
			for i := len(h.closeFuncs) - 1; i >= 0; i-- {
				h.closeFuncs[i]()
			}
		})
	}
	return nil
}
func (*Handle) String() string     { return "encryptionsetup.Handle{encryption:redacted}" }
func (h *Handle) GoString() string { return h.String() }

// Open validates descriptors, loads existing key sources, and verifies remote
// key capabilities before returning. Missing/wrong keys and unavailable backends
// fail explicitly. The caller must verify the durable catalog with the Sealer
// before enabling writes; successful construction alone cannot prove recovery.
func Open(ctx context.Context, opts Options) (*Handle, error) {
	if ctx == nil {
		return nil, secureconfig.ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.StorageMode != "raft" && opts.StorageMode != "memory" {
		return nil, ErrConfiguration
	}
	if opts.Encryption == nil && opts.StorageMode == "raft" {
		return nil, ErrEncryptionRequired
	}
	if (opts.StorageMode == "raft" || opts.Encryption != nil) && !filepath.IsAbs(opts.DataDirectory) {
		return nil, ErrConfiguration
	}
	storage := runtimeconfig.Storage{Mode: opts.StorageMode, Directory: opts.DataDirectory}
	if err := opts.Encryption.Validate(storage); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfiguration, err)
	}
	if opts.Encryption == nil {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return nil, ErrBackend
		}
		defer clear(key[:])
		wrapper, err := secureconfig.NewLocalWrapper(key[:])
		if err != nil {
			return nil, err
		}
		return finish(ctx, wrapper, nil, nil)
	}
	encryption := opts.Encryption
	switch encryption.Backend {
	case "", "local":
		source := encryption.Local
		policy := secureconfig.KeyFileOptions{DataDirectory: opts.DataDirectory, ReaderSID: source.ReaderSID}
		if source.ReaderGroupID != nil {
			group := *source.ReaderGroupID
			policy.ReaderGroupID = &group
		}
		paths := append([]string{source.ActiveKeyFile}, source.PreviousKeyFiles...)
		wrappers := make([]secureconfig.KeyWrapper, 0, len(paths))
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			policy.Path = path
			wrapper, err := secureconfig.LoadLocalKeyFile(ctx, policy)
			if err != nil {
				return nil, fmt.Errorf("%w: local key source: %w", ErrBackend, err)
			}
			wrappers = append(wrappers, wrapper)
		}
		return finish(ctx, wrappers[0], wrappers[1:], nil)
	case "openbao-transit", "vault-transit":
		wrapper, err := openTransit(ctx, *encryption.Transit, opts.DataDirectory)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBackend, err)
		}
		return finish(ctx, wrapper, nil, []func(){wrapper.CloseIdleConnections})
	case "aws-kms":
		wrapper, cleanup, err := openKMS(ctx, *encryption.AWSKMS, opts.DataDirectory)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBackend, err)
		}
		return finish(ctx, wrapper, nil, []func(){cleanup, wrapper.CloseIdleConnections})
	default:
		return nil, ErrConfiguration
	}
}
func finish(ctx context.Context, active secureconfig.KeyWrapper, previous []secureconfig.KeyWrapper, closers []func()) (*Handle, error) {
	h := &Handle{closeFuncs: closers}
	sealer, err := secureconfig.NewSealer(active, previous...)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	h.sealer = sealer
	return h, nil
}
