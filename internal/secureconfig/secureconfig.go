// Package secureconfig encrypts protected configuration before durable submission.
// Encryption performs no persistence, provider execution or logging. Separate,
// explicit local key-file helpers provision and load protected wrapping keys;
// they never activate runtime configuration or replace missing key material.
//
// Each envelope uses a fresh AES-256 data key. Authentication binds the envelope
// format, wrapping-key identity, store, resource incarnation, revision and purpose.
// Callers must retain wrapping keys needed by live records and backups. Selecting
// a new active key affects new envelopes only; it does not migrate old records.
// Temporary key buffers are cleared best effort; Go does not guarantee erasure
// of cipher key schedules or other copies from process memory.
package secureconfig

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"
)

const (
	// FormatVersion identifies the envelope algorithm and authenticated-data schema.
	FormatVersion = 1
	// MaxPlaintext bounds one protected configuration payload in bytes.
	MaxPlaintext = 1 << 20
	// MaxWrappedKey bounds opaque ciphertext returned by local or remote wrappers.
	MaxWrappedKey = 64 << 10
	maxIdentity   = 1024
	keySize       = 32
	nonceSize     = 12
	tagSize       = 16
)

var (
	ErrInvalidBinding    = errors.New("secureconfig: invalid binding")
	ErrInvalidEnvelope   = errors.New("secureconfig: invalid envelope")
	ErrUnsupportedFormat = errors.New("secureconfig: unsupported envelope format")
	ErrInvalidKey        = errors.New("secureconfig: invalid encryption key")
	ErrInvalidKeyring    = errors.New("secureconfig: invalid wrapping keyring")
	ErrMissingKey        = errors.New("secureconfig: wrapping key unavailable")
	ErrWrap              = errors.New("secureconfig: wrapping operation failed")
	ErrUnwrap            = errors.New("secureconfig: unwrapping operation failed")
	ErrAuthentication    = errors.New("secureconfig: authentication failed")
	ErrPlaintextTooLarge = errors.New("secureconfig: plaintext exceeds limit")
	ErrInvalidContext    = errors.New("secureconfig: context is required")
)

// Binding identifies the exact committed context in which plaintext is valid.
// Every field is required and limited to 1,024 UTF-8 bytes. Identity fields are
// authenticated, not encrypted; they must not contain credentials or key material.
type Binding struct {
	StoreID  string `json:"store_id"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	UID      string `json:"uid"`
	Revision string `json:"revision"`
	Purpose  string `json:"purpose"`
}

// Validate checks the binding without copying its values into error messages.
func (b Binding) Validate() error {
	for _, value := range []string{b.StoreID, b.Kind, b.ID, b.UID, b.Revision, b.Purpose} {
		if !validIdentity(value) {
			return ErrInvalidBinding
		}
	}
	return nil
}

// Envelope is the only encryption object submitted to durable storage.
// Its byte slices are owned by the caller; use Clone before sharing ownership.
type Envelope struct {
	Format     int    `json:"format"`
	KeyID      string `json:"key_id"`
	WrappedKey []byte `json:"wrapped_key"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Validate rejects unsupported formats and malformed or excessive lengths.
// This structural check does not authenticate ciphertext or prove key access.
func (e Envelope) Validate() error {
	if e.Format != FormatVersion {
		return ErrUnsupportedFormat
	}
	if !validIdentity(e.KeyID) || len(e.WrappedKey) == 0 || len(e.WrappedKey) > MaxWrappedKey ||
		len(e.Nonce) != nonceSize || len(e.Ciphertext) < tagSize || len(e.Ciphertext) > MaxPlaintext+tagSize {
		return ErrInvalidEnvelope
	}
	return nil
}

// Clone returns independently owned ciphertext slices.
func (e Envelope) Clone() Envelope {
	e.WrappedKey = slices.Clone(e.WrappedKey)
	e.Nonce = slices.Clone(e.Nonce)
	e.Ciphertext = slices.Clone(e.Ciphertext)
	return e
}

// KeyWrapper protects an exactly 32-byte data key using the supplied additional
// authenticated data. Implementations must have a stable non-secret ID, support
// concurrent calls, honor context cancellation, and neither modify nor retain
// input buffers. Returned buffers transfer ownership to the caller. Unwrap must
// authenticate before returning key material. Backend error text is not exposed
// by Sealer because providers may include sensitive request details in errors.
type KeyWrapper interface {
	ID() string
	Wrap(context.Context, []byte, []byte) ([]byte, error)
	Unwrap(context.Context, []byte, []byte) ([]byte, error)
}

// LocalWrapper holds an AES-256 wrapping key in memory. It is safe for concurrent
// calls. The caller owns provisioning, protecting and rotating the key; use fewer
// than 2^32 encryptions across all processes and restarts sharing the same key.
type LocalWrapper struct {
	id   string
	aead cipher.AEAD
}

// NewLocalWrapper copies an exactly 32-byte key and derives its stable ID.
// The input must be cryptographically random key material, not a password.
func NewLocalWrapper(key []byte) (*LocalWrapper, error) {
	if len(key) != keySize {
		return nil, ErrInvalidKey
	}
	owned := slices.Clone(key)
	defer clear(owned)
	digest := sha256.Sum256(owned)
	aead, err := newAEAD(owned)
	if err != nil {
		return nil, err
	}
	return &LocalWrapper{id: "local-sha256:" + hex.EncodeToString(digest[:]), aead: aead}, nil
}

// ID returns a non-secret key identity. A nil or uninitialized wrapper has no ID.
func (w *LocalWrapper) ID() string {
	if w == nil {
		return ""
	}
	return w.id
}

// Wrap returns a random nonce followed by authenticated encrypted data-key bytes.
func (w *LocalWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.aead == nil || len(key) != keySize {
		return nil, ErrInvalidKey
	}
	if len(aad) > MaxWrappedKey {
		return nil, ErrInvalidBinding
	}
	wrapped := w.aead.Seal(nil, nil, key, aad)
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return wrapped, nil
}

// Unwrap authenticates the nonce-prefixed wrapped data key against aad.
func (w *LocalWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.aead == nil {
		return nil, ErrInvalidKey
	}
	if len(wrapped) != nonceSize+keySize+tagSize || len(aad) > MaxWrappedKey {
		return nil, ErrInvalidEnvelope
	}
	key, err := w.aead.Open(nil, nil, wrapped, aad)
	if err != nil {
		return nil, ErrAuthentication
	}
	if err := contextError(ctx); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

// Sealer uses an immutable allowlist of wrapping keys. The active key seals new
// values; previous keys only decrypt retained envelopes. Sealer is safe for
// concurrent use when its KeyWrapper implementations satisfy their contract.
type Sealer struct {
	active   string
	wrappers map[string]KeyWrapper
}

// NewSealer freezes the keyring and rejects missing or duplicate key identities.
func NewSealer(active KeyWrapper, previous ...KeyWrapper) (*Sealer, error) {
	s := &Sealer{wrappers: make(map[string]KeyWrapper, len(previous)+1)}
	for index, wrapper := range append([]KeyWrapper{active}, previous...) {
		if wrapper == nil {
			return nil, ErrInvalidKeyring
		}
		id := wrapper.ID()
		if !validIdentity(id) {
			return nil, ErrInvalidKeyring
		}
		if _, exists := s.wrappers[id]; exists {
			return nil, ErrInvalidKeyring
		}
		s.wrappers[id] = wrapper
		if index == 0 {
			s.active = id
		}
	}
	return s, nil
}

// Seal encrypts one payload with a fresh data key and independent random nonces.
// Encryption must run before Raft submission, never during deterministic replay.
func (s *Sealer) Seal(ctx context.Context, binding Binding, plaintext []byte) (Envelope, error) {
	if err := contextError(ctx); err != nil {
		return Envelope{}, err
	}
	if err := binding.Validate(); err != nil {
		return Envelope{}, err
	}
	if len(plaintext) > MaxPlaintext {
		return Envelope{}, ErrPlaintextTooLarge
	}
	if s == nil || s.wrappers[s.active] == nil {
		return Envelope{}, ErrInvalidKeyring
	}
	key := make([]byte, keySize)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return Envelope{}, ErrInvalidKey
	}
	e := Envelope{Format: FormatVersion, KeyID: s.active}
	wrapped, err := s.wrappers[s.active].Wrap(ctx, key, associatedData("wrap", binding, e))
	if err != nil {
		return Envelope{}, wrapperError(ctx, err, ErrWrap)
	}
	if err := contextError(ctx); err != nil {
		return Envelope{}, err
	}
	if len(wrapped) == 0 || len(wrapped) > MaxWrappedKey {
		return Envelope{}, ErrInvalidEnvelope
	}
	e.WrappedKey = slices.Clone(wrapped)
	aead, err := newAEAD(key)
	if err != nil {
		return Envelope{}, err
	}
	sealed := aead.Seal(nil, nil, plaintext, associatedData("data", binding, e))
	e.Nonce, e.Ciphertext = sealed[:nonceSize:nonceSize], sealed[nonceSize:]
	if err := contextError(ctx); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Open authenticates the complete binding and returns caller-owned plaintext.
// An unknown KeyID never selects an arbitrary external key or service endpoint.
func (s *Sealer) Open(ctx context.Context, binding Binding, e Envelope) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrInvalidKeyring
	}
	wrapper := s.wrappers[e.KeyID]
	if wrapper == nil {
		return nil, ErrMissingKey
	}
	key, err := wrapper.Unwrap(ctx, e.WrappedKey, associatedData("wrap", binding, e))
	defer clear(key)
	if err != nil {
		return nil, wrapperError(ctx, err, ErrUnwrap)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	sealed := make([]byte, 0, len(e.Nonce)+len(e.Ciphertext))
	sealed = append(sealed, e.Nonce...)
	sealed = append(sealed, e.Ciphertext...)
	plaintext, err := aead.Open(nil, nil, sealed, associatedData("data", binding, e))
	if err != nil {
		return nil, ErrAuthentication
	}
	if err := contextError(ctx); err != nil {
		clear(plaintext)
		return nil, err
	}
	return plaintext, nil
}

func associatedData(purpose string, binding Binding, e Envelope) []byte {
	// Struct field order and JSON escaping are fixed by this versioned format;
	// unlike concatenated identifiers, distinct field boundaries cannot alias.
	data, _ := json.Marshal(struct {
		Domain  string  `json:"domain"`
		Format  int     `json:"format"`
		KeyID   string  `json:"key_id"`
		Binding Binding `json:"binding"`
	}{"cpra.secureconfig." + purpose, e.Format, e.KeyID, binding})
	return data
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return aead, nil
}

func validIdentity(value string) bool {
	return len(value) > 0 && len(value) <= maxIdentity && utf8.ValidString(value)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContext
	}
	return ctx.Err()
}

func wrapperError(ctx context.Context, err, fallback error) error {
	if ctxErr := contextError(ctx); ctxErr != nil {
		return ctxErr
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrAuthentication, ErrInvalidKey} {
		if errors.Is(err, known) {
			return errors.Join(fallback, known)
		}
	}
	return fallback
}
