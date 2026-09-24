package secureconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func testBinding() Binding {
	return Binding{StoreID: "store-a", Kind: "Credential", ID: "mail", UID: "incarnation-a", Revision: "7", Purpose: "configuration"}
}

func testWrapper(t *testing.T, value byte) *LocalWrapper {
	t.Helper()
	w, err := NewLocalWrapper(bytes.Repeat([]byte{value}, keySize))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func testSealer(t *testing.T, active KeyWrapper, previous ...KeyWrapper) *Sealer {
	t.Helper()
	s, err := NewSealer(active, previous...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealRoundTripAndOwnership(t *testing.T) {
	key := bytes.Repeat([]byte{1}, keySize)
	w, err := NewLocalWrapper(key)
	if err != nil {
		t.Fatal(err)
	}
	id := w.ID()
	clear(key) // The wrapping cipher must not retain the caller's key buffer.
	if w.ID() != id {
		t.Fatal("caller mutation changed wrapper identity")
	}
	s := testSealer(t, w)
	plaintext := []byte("fixture-sensitive-value")
	e, err := s.Seal(context.Background(), testBinding(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(e)
	if err != nil || bytes.Contains(encoded, plaintext) {
		t.Fatalf("invalid envelope encoding: %v", err)
	}
	var restored Envelope
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(context.Background(), testBinding(), restored)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip failed: %v", err)
	}
	clear(got)
	again, err := s.Open(context.Background(), testBinding(), e)
	if err != nil || !bytes.Equal(again, plaintext) {
		t.Fatalf("plaintext ownership leaked: %v", err)
	}
	clone := e.Clone()
	clone.Nonce[0] ^= 1
	clone.Ciphertext[0] ^= 1
	clone.WrappedKey[0] ^= 1
	if bytes.Equal(clone.Nonce, e.Nonce) || bytes.Equal(clone.Ciphertext, e.Ciphertext) || bytes.Equal(clone.WrappedKey, e.WrappedKey) {
		t.Fatal("clone aliases original ciphertext slices")
	}
	if _, err := s.Open(context.Background(), testBinding(), e); err != nil {
		t.Fatalf("mutating clone corrupted original: %v", err)
	}
}

func TestAuthenticationBindsEveryField(t *testing.T) {
	w := testWrapper(t, 1)
	s := testSealer(t, w, namedWrapper{KeyWrapper: w, id: "alias"})
	e, err := s.Seal(context.Background(), testBinding(), []byte("protected"))
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]func(*Binding){
		"store":    func(b *Binding) { b.StoreID += "-other" },
		"kind":     func(b *Binding) { b.Kind += "-other" },
		"id":       func(b *Binding) { b.ID += "-other" },
		"uid":      func(b *Binding) { b.UID += "-other" },
		"revision": func(b *Binding) { b.Revision += "-other" },
		"purpose":  func(b *Binding) { b.Purpose += "-other" },
	}
	for name, mutate := range bindings {
		t.Run(name, func(t *testing.T) {
			b := testBinding()
			mutate(&b)
			got, err := s.Open(context.Background(), b, e)
			if got != nil || !errors.Is(err, ErrAuthentication) {
				t.Fatalf("changed binding accepted: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*Envelope){
		"nonce":           func(e *Envelope) { e.Nonce[0] ^= 1 },
		"ciphertext":      func(e *Envelope) { e.Ciphertext[0] ^= 1 },
		"wrapped_key":     func(e *Envelope) { e.WrappedKey[0] ^= 1 },
		"known_key_alias": func(e *Envelope) { e.KeyID = "alias" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := e.Clone()
			mutate(&changed)
			got, err := s.Open(context.Background(), testBinding(), changed)
			if got != nil || !errors.Is(err, ErrAuthentication) {
				t.Fatalf("tampered envelope accepted: %v", err)
			}
		})
	}
	// Purpose domains must differ even for the identical resource binding.
	if _, err := w.Unwrap(context.Background(), e.WrappedKey, associatedData("data", testBinding(), e)); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("data domain reused for key wrapping: %v", err)
	}
}

func TestKeyRotationMissingAndWrongKey(t *testing.T) {
	old, active := testWrapper(t, 1), testWrapper(t, 2)
	s1 := testSealer(t, old)
	e, err := s1.Seal(context.Background(), testBinding(), []byte("retained"))
	if err != nil {
		t.Fatal(err)
	}
	s2 := testSealer(t, active, old)
	if _, err := s2.Open(context.Background(), testBinding(), e); err != nil {
		t.Fatalf("retained key cannot decrypt: %v", err)
	}
	newEnvelope, err := s2.Seal(context.Background(), testBinding(), []byte("new"))
	if err != nil || newEnvelope.KeyID != active.ID() {
		t.Fatalf("new envelope did not select active key: %v", err)
	}
	if _, err := s1.Open(context.Background(), testBinding(), newEnvelope); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("missing key error: %v", err)
	}
	wrong := testSealer(t, namedWrapper{KeyWrapper: active, id: old.ID()})
	if _, err := wrong.Open(context.Background(), testBinding(), e); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong key accepted: %v", err)
	}
}

func TestBoundsAndMalformedEnvelopes(t *testing.T) {
	s := testSealer(t, testWrapper(t, 1))
	for _, size := range []int{0, MaxPlaintext} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			plaintext := make([]byte, size)
			e, err := s.Seal(context.Background(), testBinding(), plaintext)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Open(context.Background(), testBinding(), e)
			if err != nil || !bytes.Equal(got, plaintext) {
				t.Fatalf("boundary round trip: %v", err)
			}
		})
	}
	if _, err := s.Seal(context.Background(), testBinding(), make([]byte, MaxPlaintext+1)); !errors.Is(err, ErrPlaintextTooLarge) {
		t.Fatalf("oversized plaintext: %v", err)
	}
	e, err := s.Seal(context.Background(), testBinding(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Envelope){
		"format":                  func(e *Envelope) { e.Format++ },
		"empty_key_id":            func(e *Envelope) { e.KeyID = "" },
		"long_key_id":             func(e *Envelope) { e.KeyID = strings.Repeat("x", maxIdentity+1) },
		"empty_wrapped_key":       func(e *Envelope) { e.WrappedKey = nil },
		"long_wrapped_key":        func(e *Envelope) { e.WrappedKey = make([]byte, MaxWrappedKey+1) },
		"short_local_wrapped_key": func(e *Envelope) { e.WrappedKey = []byte{1} },
		"nonce":                   func(e *Envelope) { e.Nonce = []byte{1} },
		"short_ciphertext":        func(e *Envelope) { e.Ciphertext = nil },
		"long_ciphertext":         func(e *Envelope) { e.Ciphertext = make([]byte, MaxPlaintext+tagSize+1) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := e.Clone()
			mutate(&changed)
			if got, err := s.Open(context.Background(), testBinding(), changed); got != nil || err == nil {
				t.Fatal("malformed envelope accepted")
			}
		})
	}
	for _, value := range []string{"", strings.Repeat("x", maxIdentity+1), string([]byte{0xff})} {
		b := testBinding()
		b.StoreID = value
		if _, err := s.Seal(context.Background(), b, nil); !errors.Is(err, ErrInvalidBinding) {
			t.Fatalf("invalid binding accepted: %v", err)
		}
	}
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewLocalWrapper(make([]byte, size)); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("invalid wrapping-key length accepted: %d", size)
		}
	}
	for _, keys := range [][]KeyWrapper{{nil}, {(*LocalWrapper)(nil)}, {&LocalWrapper{}}, {testWrapper(t, 1), testWrapper(t, 1)}} {
		if _, err := NewSealer(keys[0], keys[1:]...); !errors.Is(err, ErrInvalidKeyring) {
			t.Fatalf("invalid keyring accepted: %v", err)
		}
	}
}

type namedWrapper struct {
	KeyWrapper
	id string
}

func (w namedWrapper) ID() string { return w.id }

type failingWrapper struct {
	KeyWrapper
	err error
}

func (w failingWrapper) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	return nil, w.err
}
func (w failingWrapper) Unwrap(context.Context, []byte, []byte) ([]byte, error) {
	return nil, w.err
}

func TestCancellationAndRedactedErrors(t *testing.T) {
	w := testWrapper(t, 1)
	s := testSealer(t, w)
	e, err := s.Seal(context.Background(), testBinding(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range []func() error{
		func() error { _, err := s.Seal(ctx, testBinding(), nil); return err },
		func() error { _, err := s.Open(ctx, testBinding(), e); return err },
		func() error { _, err := w.Wrap(ctx, make([]byte, keySize), nil); return err },
		func() error { _, err := w.Unwrap(ctx, e.WrappedKey, nil); return err },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	if _, err := s.Seal(nil, testBinding(), nil); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("nil context: %v", err)
	}
	const sensitive = "fixture-secret-in-provider-error"
	for _, cause := range []error{errors.New(sensitive), fmt.Errorf("%s: %w", sensitive, context.DeadlineExceeded)} {
		broken := testSealer(t, failingWrapper{KeyWrapper: w, err: cause})
		for _, operation := range []func() error{
			func() error { _, err := broken.Seal(context.Background(), testBinding(), nil); return err },
			func() error { _, err := broken.Open(context.Background(), testBinding(), e); return err },
		} {
			err := operation()
			if err == nil || strings.Contains(err.Error(), sensitive) {
				t.Fatalf("backend error exposed: %v", err)
			}
			if errors.Is(cause, context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("backend deadline classification lost")
			}
		}
	}
}

// The retained buffers intentionally violate the production wrapper's no-retain
// rule so the test can observe best-effort clearing after the synchronous call.
type inspectingWrapper struct {
	KeyWrapper
	wrappedInput []byte
	unwrappedKey []byte
	afterWrap    func()
	afterUnwrap  func()
}

func (w *inspectingWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	w.wrappedInput = key
	result, err := w.KeyWrapper.Wrap(ctx, key, aad)
	if w.afterWrap != nil {
		w.afterWrap()
	}
	return result, err
}

func (w *inspectingWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	result, err := w.KeyWrapper.Unwrap(ctx, wrapped, aad)
	w.unwrappedKey = result
	if w.afterUnwrap != nil {
		w.afterUnwrap()
	}
	return result, err
}

func TestTemporaryKeysAreClearedAndLateCancellationWins(t *testing.T) {
	w := &inspectingWrapper{KeyWrapper: testWrapper(t, 1)}
	s := testSealer(t, w)
	e, err := s.Seal(context.Background(), testBinding(), []byte("fixture"))
	if err != nil || !bytes.Equal(w.wrappedInput, make([]byte, keySize)) {
		t.Fatalf("sealing did not clear temporary key: %v", err)
	}
	if _, err := s.Open(context.Background(), testBinding(), e); err != nil || !bytes.Equal(w.unwrappedKey, make([]byte, keySize)) {
		t.Fatalf("opening did not clear temporary key: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.afterWrap = cancel
	if _, err := s.Seal(ctx, testBinding(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wrapping returned an envelope: %v", err)
	}
	if !bytes.Equal(w.wrappedInput, make([]byte, keySize)) {
		t.Fatal("canceled wrapping retained data key")
	}
	ctx, cancel = context.WithCancel(context.Background())
	w.afterUnwrap = cancel
	if got, err := s.Open(ctx, testBinding(), e); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled unwrapping returned plaintext: %v", err)
	}
	if !bytes.Equal(w.unwrappedKey, make([]byte, keySize)) {
		t.Fatal("canceled unwrapping retained data key")
	}
}

type observedWrapper struct {
	KeyWrapper
	mu   sync.Mutex
	keys map[[keySize]byte]bool
}

func (w *observedWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	w.mu.Lock()
	identity := [keySize]byte(key)
	if w.keys[identity] {
		w.mu.Unlock()
		return nil, errors.New("reused data key")
	}
	w.keys[identity] = true
	w.mu.Unlock()
	return w.KeyWrapper.Wrap(ctx, key, aad)
}

func TestConcurrentSealingUsesIndependentDataKeys(t *testing.T) {
	w := &observedWrapper{KeyWrapper: testWrapper(t, 1), keys: make(map[[keySize]byte]bool)}
	s := testSealer(t, w)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			b := testBinding()
			b.Revision = fmt.Sprint(i)
			for range 4 {
				plaintext := []byte("same plaintext")
				e, err := s.Seal(context.Background(), b, plaintext)
				if err != nil {
					t.Error(err)
					return
				}
				got, err := s.Open(context.Background(), b, e)
				if err != nil || !bytes.Equal(got, plaintext) {
					t.Errorf("concurrent round trip: %v", err)
				}
			}
		})
	}
	wg.Wait()
	if len(w.keys) != 128 {
		t.Fatalf("expected independent keys for every envelope, got %d", len(w.keys))
	}
}
