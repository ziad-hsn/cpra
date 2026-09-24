package encryptionsetup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
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

func TestKMSFactoryUsesOfficialSelectedProfileAndExactARN(t *testing.T) {
	opts, key := fixture(t)
	if _, err := secureconfig.GenerateLocalKeyFile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	// These files are deliberately different: selection and credentials-file
	// precedence must come from the production AWS loader, not a test provider.
	configPath := filepath.Join(filepath.Dir(key.Path), "aws-config")
	credentialPath := filepath.Join(filepath.Dir(key.Path), "aws-credentials")
	if err := os.WriteFile(configPath, []byte("[profile selected]\nregion = us-west-2\naws_access_key_id = config-source\naws_secret_access_key = config-source-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("[default]\naws_access_key_id = wrong-profile\naws_secret_access_key = wrong-profile-secret\n[selected]\naws_access_key_id = selected-fixture-access\naws_secret_access_key = selected-fixture-secret\naws_session_token = selected-fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, variable := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_CA_BUNDLE"} {
		t.Setenv(variable, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	const arn = "arn:aws:kms:eu-west-1:123456789012:key/12345678-1234-1234-1234-123456789012"
	const host = "kms.eu-west-1.amazonaws.com"
	block, err := aes.NewCipher(bytes.Repeat([]byte{51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != host || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=selected-fixture-access/") || !strings.Contains(r.Header.Get("Authorization"), "/eu-west-1/kms/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "selected-fixture-token" {
			t.Error("request did not use selected official profile, region and signing path")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		var input struct {
			KeyID      string            `json:"KeyId"`
			Plaintext  []byte            `json:"Plaintext"`
			Ciphertext []byte            `json:"CiphertextBlob"`
			Context    map[string]string `json:"EncryptionContext"`
			Algorithm  string            `json:"EncryptionAlgorithm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.KeyID != arn {
			t.Error("request did not pin the exact key ARN")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		operation := r.Header.Get("X-Amz-Target")
		if operation == "TrentService.DescribeKey" {
			_ = json.NewEncoder(w).Encode(map[string]any{"KeyMetadata": map[string]any{"Arn": arn, "Enabled": true, "KeyState": "Enabled", "KeyManager": "CUSTOMER", "KeySpec": "SYMMETRIC_DEFAULT", "KeyUsage": "ENCRYPT_DECRYPT", "EncryptionAlgorithms": []string{"SYMMETRIC_DEFAULT"}}})
			return
		}
		if input.Algorithm != "SYMMETRIC_DEFAULT" || len(input.Context) != 2 || input.Context["cpra:purpose"] != "configuration-wrap-v1" || len(input.Context["cpra:binding-sha256"]) != 64 {
			t.Error("missing encryption binding")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		aad, _ := json.Marshal(input.Context)
		switch operation {
		case "TrentService.Encrypt":
			nonce := make([]byte, aead.NonceSize())
			_, _ = rand.Read(nonce)
			wrapped := aead.Seal(nonce, nonce, input.Plaintext, aad)
			_ = json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": wrapped, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		case "TrentService.Decrypt":
			if len(input.Ciphertext) < aead.NonceSize() {
				http.Error(w, "invalid fixture ciphertext", http.StatusBadRequest)
				return
			}
			plain, err := aead.Open(nil, input.Ciphertext[:aead.NonceSize()], input.Ciphertext[aead.NonceSize():], aad)
			if err != nil {
				http.Error(w, "invalid fixture ciphertext", http.StatusBadRequest)
				return
			}
			defer clear(plain)
			_ = json.NewEncoder(w).Encode(map[string]any{"Plaintext": plain, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		default:
			http.Error(w, "unexpected fixture operation", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	// Only socket routing and test trust roots change. The actual factory,
	// profile loading, AWS endpoint resolver, signer and decoder all execute.
	// No request can escape to an account or other endpoint from this fixture.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != host+":443" {
			return nil, errors.New("fixture rejected unexpected endpoint")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	})
	opts.Encryption = &runtimeconfig.ManagementEncryption{Backend: "aws-kms", AWSKMS: &runtimeconfig.ManagementAWSKMS{KeyARN: arn, Region: "eu-west-1", Profile: "selected", SharedConfigFiles: []string{configPath}, SharedCredentialsFiles: []string{credentialPath}}}
	handle, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	sealed, err := handle.Sealer().Seal(context.Background(), binding(), []byte("private fixture configuration"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KeyID != "aws-kms:"+arn {
		t.Fatal("active identity did not preserve the exact ARN")
	}
	plain, err := handle.Sealer().Open(context.Background(), binding(), sealed)
	if err != nil || string(plain) != "private fixture configuration" {
		t.Fatal("round trip", err)
	}
	clear(plain)
	if requests.Load() != 3 {
		t.Fatal("unexpected provider operations", requests.Load())
	}
	if _, err := os.Stat(opts.DataDirectory); !os.IsNotExist(err) {
		t.Fatal("factory created durable storage", err)
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedBody struct {
	io.Reader
	closes int
}

func (b *countedBody) Close() error { b.closes++; return nil }

func TestCredentialTransportBoundsAndReleasesResponses(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://credential.example", nil)
	for _, size := range []int{0, 256 << 10, (256 << 10) + 1} {
		body := &countedBody{Reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, size))}
		transport := &credentialTransport{base: testTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		})}
		response, err := transport.RoundTrip(request)
		if body.closes != 1 {
			t.Fatal("original credential body not closed")
		}
		if size > 256<<10 {
			if !errors.Is(err, ErrBackend) || response != nil {
				t.Fatal("overflow was silently truncated", err)
			}
			continue
		}
		if err != nil || response == nil {
			t.Fatal("bounded credential response rejected", err)
		}
		owned := response.Body.(*credentialBody)
		reference := owned.data
		decoded, err := io.ReadAll(response.Body)
		if err != nil || len(decoded) != size {
			t.Fatal("credential response altered", err)
		}
		clear(decoded)
		if err := response.Body.Close(); err != nil || !bytes.Equal(reference, make([]byte, size)) {
			t.Fatal("owned credential bytes retained after close", err)
		}
	}
	want := errors.New("fixture read error")
	body := &countedBody{Reader: failingReader{err: want}}
	transport := &credentialTransport{base: testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})}
	if response, err := transport.RoundTrip(request); !errors.Is(err, want) || response != nil || body.closes != 1 {
		t.Fatal("failed credential read leaked body", err)
	}
	transport.base = testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{Body: body}, want
	})
	if response, err := transport.RoundTrip(request); !errors.Is(err, want) || response != nil || body.closes != 2 {
		t.Fatal("failed roundtrip leaked body", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
