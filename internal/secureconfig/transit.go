package secureconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TransitOptions selects an existing OpenBao/Vault Transit AES-256-GCM key.
// Address is one HTTPS origin, Mount may have slash-separated segments, and Key
// is one name. TokenSource owns protected-file/token refresh and must honor its
// context. It is called on each request and must support concurrent callers.
// Credentials and token-file handling remain outside this adapter and Raft.
type TransitOptions struct {
	Address     string
	Mount       string
	Key         string
	Namespace   string
	TokenSource func(context.Context) (string, error)
	HTTPClient  *http.Client
	Timeout     time.Duration
}

// TransitWrapper invokes only key inspection, encrypt and decrypt. It never
// creates, rotates, exports or deletes keys. Policies must grant read on keys/name
// and update (without create) on encrypt/name and decrypt/name to prevent upsert.
//
// ID identifies the configured origin/namespace/mount/name, not a provider key
// UUID. Keep this key and mount identity intact. Provider-internal key versions
// remain in each wrapped ciphertext; rotation does not change the wrapper ID.
// Moving to another wrapper identity requires Open+Seal because Envelope.KeyID
// is authenticated by both layers; this adapter provides no KEK-only rewrap API.
type TransitWrapper struct {
	id          string
	base        string
	key         string
	namespace   string
	tokenSource func(context.Context) (string, error)
	client      *http.Client
	timeout     time.Duration
}

// TransitError reports only an HTTP status, without provider body, URL or token.
type TransitError struct{ StatusCode int }

func (err *TransitError) Error() string {
	return "secureconfig: Transit request rejected (HTTP " + strconv.Itoa(err.StatusCode) + ")"
}
func (err *TransitError) Is(target error) bool { return target == ErrRemoteUnavailable }

// NewTransitWrapper verifies key capability and performs a random wrapping-key
// challenge: correct AAD must decrypt; modified AAD must receive an HTTP 400.
// This prevents silently using an older server which ignores associated_data.
// Initialization therefore requires key-read, encrypt and decrypt permission.
func NewTransitWrapper(ctx context.Context, opts TransitOptions) (*TransitWrapper, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	origin, err := wrappingOrigin(opts.Address)
	if err != nil || !wrappingPath(opts.Mount, true) || !wrappingPath(opts.Key, false) || (opts.Namespace != "" && !wrappingPath(opts.Namespace, true)) || opts.TokenSource == nil {
		return nil, ErrRemoteConfiguration
	}
	timeout, err := remoteTimeout(opts.Timeout)
	if err != nil {
		return nil, err
	}
	client, err := privateWrappingHTTPClient(opts.HTTPClient, timeout)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			client.CloseIdleConnections()
		}
	}()
	identity, _ := json.Marshal([]string{"cpra-transit-v1", origin.String(), opts.Namespace, opts.Mount, opts.Key, "aes256-gcm96"})
	digest := sha256.Sum256(identity)
	w := &TransitWrapper{id: "transit-sha256:" + hex.EncodeToString(digest[:]), base: origin.String() + "/v1/" + opts.Mount, key: opts.Key, namespace: opts.Namespace, tokenSource: opts.TokenSource, client: client, timeout: timeout}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var metadata struct {
		Data *struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			Derived    *bool  `json:"derived"`
			Convergent bool   `json:"convergent_encryption"`
			Encrypt    bool   `json:"supports_encryption"`
			Decrypt    bool   `json:"supports_decryption"`
		} `json:"data"`
	}
	if err := w.request(ctx, http.MethodGet, "keys", nil, &metadata); err != nil {
		return nil, err
	}
	m := metadata.Data
	if m == nil || m.Name != opts.Key || m.Type != "aes256-gcm96" || m.Derived == nil || *m.Derived || m.Convergent || !m.Encrypt || !m.Decrypt {
		return nil, ErrRemoteKeyUnsupported
	}
	if err := w.verifyAAD(ctx); err != nil {
		return nil, err
	}
	initialized = true
	return w, nil
}

// CloseIdleConnections releases this adapter's owned idle connections. It does
// not close a caller-owned custom transport or interrupt active operations.
func (w *TransitWrapper) CloseIdleConnections() {
	if w != nil && w.client != nil {
		w.client.CloseIdleConnections()
	}
}

func (w *TransitWrapper) ID() string {
	if w == nil {
		return ""
	}
	return w.id
}
func (w *TransitWrapper) String() string   { return "TransitWrapper{configuration:redacted}" }
func (w *TransitWrapper) GoString() string { return w.String() }

func (w *TransitWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.client == nil || len(key) != keySize {
		return nil, ErrInvalidKey
	}
	if len(aad) > MaxWrappedKey {
		return nil, ErrInvalidBinding
	}
	var response struct {
		Data *struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	err := w.request(ctx, http.MethodPost, "encrypt", struct {
		Plaintext []byte `json:"plaintext"`
		AAD       []byte `json:"associated_data"`
	}{key, aad}, &response)
	if err != nil {
		return nil, err
	}
	if response.Data == nil || !validTransitCiphertext(response.Data.Ciphertext) {
		return nil, ErrRemoteResponse
	}
	return []byte(response.Data.Ciphertext), nil
}

func (w *TransitWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.client == nil {
		return nil, ErrInvalidKey
	}
	if len(wrapped) > MaxWrappedKey || !validTransitCiphertext(string(wrapped)) {
		return nil, ErrInvalidEnvelope
	}
	if len(aad) > MaxWrappedKey {
		return nil, ErrInvalidBinding
	}
	var response struct {
		Data *struct {
			Plaintext []byte `json:"plaintext"`
		} `json:"data"`
	}
	err := w.request(ctx, http.MethodPost, "decrypt", struct {
		Ciphertext string `json:"ciphertext"`
		AAD        []byte `json:"associated_data"`
	}{string(wrapped), aad}, &response)
	if err != nil {
		if response.Data != nil {
			clear(response.Data.Plaintext)
		}
		return nil, err
	}
	if response.Data == nil || len(response.Data.Plaintext) != keySize {
		if response.Data != nil {
			clear(response.Data.Plaintext)
		}
		return nil, ErrRemoteResponse
	}
	return response.Data.Plaintext, nil
}

func (w *TransitWrapper) verifyAAD(ctx context.Context) error {
	var key [keySize]byte
	defer clear(key[:])
	_, _ = rand.Read(key[:])
	aad := []byte("cpra-transit-aad-capability-v1/" + rand.Text())
	wrapped, err := w.Wrap(ctx, key[:], aad)
	if err != nil {
		return err
	}
	plain, err := w.Unwrap(ctx, wrapped, aad)
	if err != nil {
		return err
	}
	valid := bytes.Equal(plain, key[:])
	clear(plain)
	if !valid {
		return ErrAssociatedDataUnsupported
	}
	aad[0] ^= 1
	plain, err = w.Unwrap(ctx, wrapped, aad)
	clear(plain)
	var rejected *TransitError
	if err == nil || !errors.As(err, &rejected) || rejected.StatusCode != http.StatusBadRequest {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrAssociatedDataUnsupported
	}
	return nil
}

func (w *TransitWrapper) request(ctx context.Context, method, operation string, payload, output any) error {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	token, err := w.tokenSource(ctx)
	if err != nil {
		return remoteOperationError(ctx, err)
	}
	if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
		return ErrRemoteConfiguration
	}
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return ErrRemoteConfiguration
		}
		defer clear(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, w.base+"/"+operation+"/"+w.key, bytes.NewReader(body))
	if err != nil {
		return ErrRemoteConfiguration
	}
	request.Header.Set("X-Vault-Token", token)
	if w.namespace != "" {
		request.Header.Set("X-Vault-Namespace", w.namespace)
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := w.client.Do(request)
	if err != nil {
		return remoteOperationError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &TransitError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(output); err != nil {
		return ErrRemoteResponse
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrRemoteResponse
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func validTransitCiphertext(value string) bool {
	if len(value) > MaxWrappedKey || !strings.HasPrefix(value, "vault:v") {
		return false
	}
	version, payload, ok := strings.Cut(value[len("vault:v"):], ":")
	if !ok || len(version) == 0 || len(version) > 10 {
		return false
	}
	n, err := strconv.ParseUint(version, 10, 32)
	if err != nil || n == 0 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	return err == nil && len(decoded) == nonceSize+keySize+tagSize
}
