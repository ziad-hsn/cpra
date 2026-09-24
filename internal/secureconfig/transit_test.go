package secureconfig

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This is an HTTPS wire-contract fixture using independent AES-GCM operations.
// It is not an OpenBao/Vault installation or proof about a production account.
type transitFixture struct {
	server     *httptest.Server
	cipher     cipher.AEAD
	ignoreAAD  bool
	keyType    string
	derived    bool
	convergent bool
	requests   atomic.Int64
	token      atomic.Value
	mode       atomic.Value
}

func newTransitFixture(t *testing.T, configure func(*transitFixture)) *transitFixture {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{39}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	f := &transitFixture{cipher: aead, keyType: "aes256-gcm96"}
	f.token.Store("fixture-token-first")
	f.mode.Store("")
	if configure != nil {
		configure(f)
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *transitFixture) options() TransitOptions {
	return TransitOptions{Address: f.server.URL, Mount: "team/transit", Key: "configuration", Namespace: "operators/cpra", HTTPClient: f.server.Client(), TokenSource: func(context.Context) (string, error) { return f.token.Load().(string), nil }}
}

func (f *transitFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if r.Header.Get("X-Vault-Token") != f.token.Load().(string) || r.Header.Get("X-Vault-Namespace") != "operators/cpra" {
		http.Error(w, "fixture-provider-private-error", 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch f.mode.Load().(string) {
	case "large":
		fmt.Fprint(w, strings.Repeat("x", remoteResponseLimit+1))
		return
	case "error":
		http.Error(w, "fixture-provider-private-error", 503)
		return
	case "slow":
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		return
	case "trailing":
		fmt.Fprint(w, `{"data":{}} {"more":true}`)
		return
	}
	if r.URL.Path == "/v1/team/transit/keys/configuration" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "configuration", "type": f.keyType, "derived": f.derived, "convergent_encryption": f.convergent, "supports_encryption": true, "supports_decryption": true}})
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "unexpected fixture request", 400)
		return
	}
	var input struct {
		Plaintext  []byte `json:"plaintext"`
		AAD        []byte `json:"associated_data"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid fixture JSON", 400)
		return
	}
	if f.ignoreAAD {
		input.AAD = nil
	}
	switch r.URL.Path {
	case "/v1/team/transit/encrypt/configuration":
		if len(input.Plaintext) != 32 {
			http.Error(w, "invalid fixture plaintext", 400)
			return
		}
		nonce := make([]byte, f.cipher.NonceSize())
		_, _ = rand.Read(nonce)
		data := f.cipher.Seal(nonce, nonce, input.Plaintext, input.AAD)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString(data)}})
	case "/v1/team/transit/decrypt/configuration":
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(input.Ciphertext, "vault:v1:"))
		if err != nil || len(data) < f.cipher.NonceSize() {
			http.Error(w, "invalid fixture ciphertext", 400)
			return
		}
		plain, err := f.cipher.Open(nil, data[:f.cipher.NonceSize()], data[f.cipher.NonceSize():], input.AAD)
		if err != nil {
			http.Error(w, "fixture-provider-private-authentication-error", 400)
			return
		}
		defer clear(plain)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string][]byte{"plaintext": plain}})
	default:
		http.Error(w, "unexpected fixture endpoint", 404)
	}
}

func TestTransitTLSContractAndBoundIdentity(t *testing.T) {
	f := newTransitFixture(t, nil)
	options := f.options()
	originalTransport := options.HTTPClient.Transport
	w, err := NewTransitWrapper(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != 4 {
		t.Fatalf("expected metadata + AAD challenge, got %d requests", f.requests.Load())
	}
	if options.HTTPClient.CheckRedirect != nil || options.HTTPClient.Transport != originalTransport {
		t.Fatal("constructor mutated caller HTTP client")
	}
	sealer := testSealer(t, w)
	envelope, err := sealer.Seal(context.Background(), testBinding(), []byte("private configuration"))
	if err != nil {
		t.Fatal(err)
	}
	f.token.Store("fixture-token-rotated")
	plain, err := sealer.Open(context.Background(), testBinding(), envelope)
	if err != nil || string(plain) != "private configuration" {
		t.Fatalf("rotated token/decrypt: %v", err)
	}
	modified := testBinding()
	modified.Revision = "another-revision"
	if _, err := sealer.Open(context.Background(), modified, envelope); !errors.Is(err, ErrUnwrap) {
		t.Fatalf("AAD tamper accepted: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", w, w), f.server.URL) {
		t.Fatal("wrapper formatting exposes config")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := sealer.Seal(context.Background(), testBinding(), []byte("concurrent"))
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := sealer.Open(context.Background(), testBinding(), e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestTransitRejectsUnsupportedKeyOrIgnoredAAD(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*transitFixture)
		want      error
	}{
		{"aes128", func(f *transitFixture) { f.keyType = "aes128-gcm96" }, ErrRemoteKeyUnsupported},
		{"derived", func(f *transitFixture) { f.derived = true }, ErrRemoteKeyUnsupported},
		{"convergent", func(f *transitFixture) { f.convergent = true }, ErrRemoteKeyUnsupported},
		{"ignored_aad", func(f *transitFixture) { f.ignoreAAD = true }, ErrAssociatedDataUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newTransitFixture(t, test.configure)
			if _, err := NewTransitWrapper(context.Background(), f.options()); !errors.Is(err, test.want) {
				t.Fatalf("want %v, got %v", test.want, err)
			}
		})
	}
}

func TestTransitBoundsAndCancellation(t *testing.T) {
	f := newTransitFixture(t, nil)
	w, err := NewTransitWrapper(context.Background(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"large", "error", "trailing"} {
		f.mode.Store(mode)
		before := f.requests.Load()
		_, err := w.Wrap(context.Background(), bytes.Repeat([]byte{1}, 32), []byte("binding"))
		if err == nil || strings.Contains(err.Error(), "fixture-provider-private") {
			t.Fatalf("unsafe error: %v", err)
		}
		if mode == "large" && !errors.Is(err, ErrRemoteResponseTooLarge) {
			t.Fatal(err)
		}
		if f.requests.Load() != before+1 {
			t.Fatal("backend failure was retried")
		}
	}
	f.mode.Store("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := f.requests.Load()
	if _, err := w.Wrap(ctx, bytes.Repeat([]byte{1}, 32), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.requests.Load() != before {
		t.Fatal("canceled operation reached backend")
	}
	f.mode.Store("slow")
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := w.Wrap(ctx, bytes.Repeat([]byte{1}, 32), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestTransitRejectsOriginPathAndRedirect(t *testing.T) {
	f := newTransitFixture(t, nil)
	for _, change := range []func(*TransitOptions){
		func(o *TransitOptions) { o.Address = "http://example.com" }, func(o *TransitOptions) { o.Address = "https://user:password@example.com" }, func(o *TransitOptions) { o.Address = "https://example.com/path" }, func(o *TransitOptions) { o.Address = "https://example.com?token=private" }, func(o *TransitOptions) { o.Mount = "../transit" }, func(o *TransitOptions) { o.Key = "key/other" }, func(o *TransitOptions) { o.Namespace = "private\r\nHeader:bad" },
	} {
		opts := f.options()
		change(&opts)
		if _, err := NewTransitWrapper(context.Background(), opts); !errors.Is(err, ErrRemoteConfiguration) {
			t.Fatalf("bad options accepted: %v", err)
		}
	}
	var received atomic.Int64
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer other.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	opts := f.options()
	opts.Address = redirect.URL
	opts.HTTPClient = redirect.Client()
	_, err := NewTransitWrapper(context.Background(), opts)
	var rejected *TransitError
	if !errors.As(err, &rejected) || rejected.StatusCode != 307 || received.Load() != 0 {
		t.Fatalf("redirect boundary failed: %v", err)
	}
}
