package encryptionsetup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestTransitFactoryProtectedTokenRotationAndCA(t *testing.T) {
	opts, key := fixture(t)
	if _, err := secureconfig.GenerateLocalKeyFile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	token := "fixture-token-first"
	if err := os.WriteFile(key.Path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var expected atomic.Value
	expected.Store(token)
	var calls atomic.Int32
	// This is a local TLS wire fixture, not an installed or account-certified
	// OpenBao/Vault server. The production factory and wrapper execute unchanged.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Vault-Token") != expected.Load().(string) || r.Header.Get("X-Vault-Namespace") != "team" {
			http.Error(w, "private fixture details", 403)
			return
		}
		if r.URL.Path == "/v1/transit/keys/cpra" {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "cpra", "type": "aes256-gcm96", "derived": false, "supports_encryption": true, "supports_decryption": true}})
			return
		}
		var input struct {
			Plaintext  []byte `json:"plaintext"`
			AAD        []byte `json:"associated_data"`
			Ciphertext string `json:"ciphertext"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		switch r.URL.Path {
		case "/v1/transit/encrypt/cpra":
			nonce := make([]byte, aead.NonceSize())
			rand.Read(nonce)
			value := "vault:v1:" + base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, input.Plaintext, input.AAD))
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ciphertext": value}})
		case "/v1/transit/decrypt/cpra":
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(input.Ciphertext, "vault:v1:"))
			if err != nil || len(raw) < aead.NonceSize() {
				http.Error(w, "invalid", 400)
				return
			}
			plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], input.AAD)
			if err != nil {
				http.Error(w, "invalid", 400)
				return
			}
			defer clear(plain)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"plaintext": plain}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ca := filepath.Join(filepath.Dir(key.Path), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	source := &runtimeconfig.ManagementTransit{Address: server.URL, Mount: "transit", Key: "cpra", TokenFile: key.Path, CAFile: ca, Namespace: "team"}
	opts.Encryption = &runtimeconfig.ManagementEncryption{Backend: "openbao-transit", Transit: source}
	handle, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if calls.Load() != 4 {
		t.Fatal("capability and AAD contract not verified", calls.Load())
	}
	sealed, err := handle.Sealer().Seal(context.Background(), binding(), []byte("fixture resource secret"))
	if err != nil {
		t.Fatal(err)
	}
	token = "fixture-token-second"
	expected.Store(token)
	// Replace token atomically inside the same protected directory.
	replacement := key.Path + ".next"
	if err := os.WriteFile(replacement, []byte(token+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, key.Path); err != nil {
		t.Fatal(err)
	}
	plain, err := handle.Sealer().Open(context.Background(), binding(), sealed)
	if err != nil || string(plain) != "fixture resource secret" {
		t.Fatal("token rotation", err)
	}
	clear(plain)
	// A later unsafe permission change is caught before making another request.
	if err := os.Chmod(key.Path, 0644); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := handle.Sealer().Open(context.Background(), binding(), sealed); err == nil {
		t.Fatal("token protection stopped being checked")
	}
	if calls.Load() != before {
		t.Fatal("request sent using unsafe token")
	}
}

func TestProtectedTokenAndCARedaction(t *testing.T) {
	_, key := fixture(t)
	if _, err := secureconfig.GenerateLocalKeyFile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	policy := secureconfig.ProtectedFileOptions{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 8194}
	for _, data := range [][]byte{nil, []byte(" \n"), []byte("secret-token\nembedded"), []byte("secret-token\x00"), bytes.Repeat([]byte{'x'}, 8195)} {
		if err := os.WriteFile(key.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := tokenSource(policy)(context.Background())
		if err == nil || strings.Contains(err.Error(), "secret-token") {
			t.Fatal("invalid token accepted or exposed", err)
		}
	}
	for _, data := range [][]byte{nil, []byte("private-invalid-certificate"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("private-invalid-certificate")})} {
		if err := os.WriteFile(key.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := certificatePool(context.Background(), policy)
		if !errors.Is(err, ErrConfiguration) || strings.Contains(err.Error(), "private-invalid-certificate") {
			t.Fatal("CA accepted or leaked", err)
		}
	}
}
