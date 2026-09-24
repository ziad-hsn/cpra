package secureconfig

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go/logging"
)

const fixtureKMSARN = "arn:aws:kms:eu-west-1:123456789012:key/12345678-1234-1234-1234-123456789012"

type kmsFixture struct {
	server   *httptest.Server
	cipher   cipher.AEAD
	metadata map[string]any
	mode     atomic.Value
	requests atomic.Int64
	signed   atomic.Int64
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (roundtrip testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundtrip(request)
}

// Requests still pass through the production AWS SDK serializer, signer,
// endpoint resolver, middleware and response decoder. Only the caller-owned
// transport routes the signed request to a local TLS protocol fixture.
func newKMSFixture(t *testing.T, configure func(*kmsFixture)) (*kmsFixture, aws.Config) {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{57}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	f := &kmsFixture{cipher: aead, metadata: map[string]any{"Arn": fixtureKMSARN, "Enabled": true, "KeyState": "Enabled", "KeyManager": "CUSTOMER", "KeySpec": "SYMMETRIC_DEFAULT", "KeyUsage": "ENCRYPT_DECRYPT", "EncryptionAlgorithms": []string{"SYMMETRIC_DEFAULT"}}}
	f.mode.Store("")
	if configure != nil {
		configure(f)
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	target, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tlsTransport := f.server.Client().Transport
	transport := testRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "kms.eu-west-1.amazonaws.com" {
			return nil, errors.New("fixture observed unexpected endpoint")
		}
		if !strings.HasPrefix(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=fixture-access/") || request.Header.Get("X-Amz-Date") == "" {
			return nil, errors.New("fixture did not receive signed SDK request")
		}
		f.signed.Add(1)
		copy := request.Clone(request.Context())
		uri := *request.URL
		copy.URL = &uri
		copy.Host = request.URL.Host
		copy.URL.Scheme = target.Scheme
		copy.URL.Host = target.Host
		return tlsTransport.RoundTrip(copy)
	})
	return f, aws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access", "fixture-aws-secret", ""), HTTPClient: &http.Client{Transport: transport}}
}

func (f *kmsFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch f.mode.Load().(string) {
	case "error":
		w.WriteHeader(503)
		fmt.Fprint(w, `{"__type":"KMSInternalException","message":"fixture-sensitive-backend-error"}`)
		return
	case "large":
		fmt.Fprint(w, strings.Repeat("x", remoteResponseLimit+1))
		return
	case "slow":
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		return
	case "redirect":
		w.Header().Set("Location", f.server.URL+"/redirected")
		w.WriteHeader(307)
		return
	}
	var input struct {
		KeyID      string            `json:"KeyId"`
		Plaintext  []byte            `json:"Plaintext"`
		Ciphertext []byte            `json:"CiphertextBlob"`
		Context    map[string]string `json:"EncryptionContext"`
		Algorithm  string            `json:"EncryptionAlgorithm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.KeyID != fixtureKMSARN {
		http.Error(w, "invalid fixture request", 400)
		return
	}
	operation := r.Header.Get("X-Amz-Target")
	if operation == "TrentService.DescribeKey" {
		_ = json.NewEncoder(w).Encode(map[string]any{"KeyMetadata": f.metadata})
		return
	}
	if input.Algorithm != "SYMMETRIC_DEFAULT" || len(input.Context) != 2 || input.Context["cpra:purpose"] != "configuration-wrap-v1" || len(input.Context["cpra:binding-sha256"]) != 64 {
		http.Error(w, "invalid fixture encryption context", 400)
		return
	}
	aad, _ := json.Marshal(input.Context)
	keyID := fixtureKMSARN
	if f.mode.Load().(string) == "wrong-key" {
		keyID = strings.Replace(fixtureKMSARN, "12345678-", "aaaaaaaa-", 1)
	}
	switch operation {
	case "TrentService.Encrypt":
		nonce := make([]byte, f.cipher.NonceSize())
		_, _ = rand.Read(nonce)
		wrapped := f.cipher.Seal(nonce, nonce, input.Plaintext, aad)
		_ = json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": wrapped, "KeyId": keyID, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
	case "TrentService.Decrypt":
		if len(input.Ciphertext) < f.cipher.NonceSize() {
			http.Error(w, "invalid fixture ciphertext", 400)
			return
		}
		plain, err := f.cipher.Open(nil, input.Ciphertext[:f.cipher.NonceSize()], input.Ciphertext[f.cipher.NonceSize():], aad)
		if err != nil {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"__type":"InvalidCiphertextException","message":"fixture-sensitive-backend-error"}`)
			return
		}
		defer clear(plain)
		if f.mode.Load().(string) == "bad-plaintext" {
			plain = append(plain, byte(1))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Plaintext": plain, "KeyId": keyID, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
	default:
		http.Error(w, "unexpected fixture operation", 400)
	}
}

func TestKMSSignedTLSContractAndIdentity(t *testing.T) {
	f, config := newKMSFixture(t, nil)
	var logs bytes.Buffer
	config.Logger = logging.LoggerFunc(func(_ logging.Classification, format string, args ...interface{}) {
		fmt.Fprintf(&logs, format, args...)
	})
	config.ClientLogMode = aws.LogRequestWithBody | aws.LogResponseWithBody | aws.LogSigning
	w, err := NewKMSWrapper(context.Background(), config, KMSOptions{KeyARN: fixtureKMSARN})
	if err != nil {
		t.Fatal(err)
	}
	if w.ID() != "aws-kms:"+fixtureKMSARN {
		t.Fatal("wrapper did not record exact ARN")
	}
	sealer := testSealer(t, w)
	envelope, err := sealer.Seal(context.Background(), testBinding(), []byte("provider-private-value"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := sealer.Open(context.Background(), testBinding(), envelope)
	if err != nil || string(plain) != "provider-private-value" {
		t.Fatalf("decrypt: %v", err)
	}
	changed := testBinding()
	changed.ID = "different-resource"
	if _, err := sealer.Open(context.Background(), changed, envelope); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("context tamper: %v", err)
	}
	if f.signed.Load() != f.requests.Load() {
		t.Fatal("SDK signing path was bypassed")
	}
	if logs.Len() != 0 {
		t.Fatal("caller SDK body logging settings leaked into wrapper")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", w, w), fixtureKMSARN) {
		t.Fatal("formatting exposed config")
	}
	for _, value := range kmsBindingContext([]byte("private-binding-marker")) {
		if strings.Contains(value, "private-binding-marker") {
			t.Fatal("raw binding in CloudTrail-visible encryption context")
		}
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

func TestKMSRejectsKeyPoliciesAndAliases(t *testing.T) {
	for _, test := range []struct {
		field string
		value any
	}{{"KeyManager", "AWS"}, {"KeyState", "Disabled"}, {"Enabled", false}, {"KeySpec", "RSA_2048"}, {"KeyUsage", "SIGN_VERIFY"}, {"Arn", "arn:unexpected"}, {"EncryptionAlgorithms", []string{"RSAES_OAEP_SHA_256"}}} {
		t.Run(test.field, func(t *testing.T) {
			_, config := newKMSFixture(t, func(f *kmsFixture) { f.metadata[test.field] = test.value })
			if _, err := NewKMSWrapper(context.Background(), config, KMSOptions{KeyARN: fixtureKMSARN}); !errors.Is(err, ErrRemoteKeyUnsupported) {
				t.Fatalf("unsupported key accepted: %v", err)
			}
		})
	}
	f, config := newKMSFixture(t, nil)
	for _, arn := range []string{"alias/configuration", strings.Replace(fixtureKMSARN, ":key/", ":alias/", 1), strings.Replace(fixtureKMSARN, "eu-west-1", "us-east-1", 1), strings.Replace(fixtureKMSARN, "123456789012", "notanaccount", 1)} {
		if _, err := NewKMSWrapper(context.Background(), config, KMSOptions{KeyARN: arn}); !errors.Is(err, ErrRemoteConfiguration) {
			t.Fatalf("bad ARN accepted: %v", err)
		}
	}
	if f.requests.Load() != 0 {
		t.Fatal("invalid ARN validation contacted backend")
	}
}

func TestKMSBoundsErrorsAndCancellation(t *testing.T) {
	f, config := newKMSFixture(t, nil)
	w, err := NewKMSWrapper(context.Background(), config, KMSOptions{KeyARN: fixtureKMSARN})
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{19}, 32)
	wrapped, err := w.Wrap(context.Background(), key, []byte("binding"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"error", "large", "redirect", "wrong-key"} {
		f.mode.Store(mode)
		before := f.requests.Load()
		_, err := w.Wrap(context.Background(), key, []byte("binding"))
		if err == nil || strings.Contains(err.Error(), "fixture-sensitive") {
			t.Fatalf("unsafe error: %v", err)
		}
		if mode == "large" && !errors.Is(err, ErrRemoteResponseTooLarge) {
			t.Fatal(err)
		}
		if f.requests.Load() != before+1 {
			t.Fatalf("failure retried or redirected: %s", mode)
		}
	}
	f.mode.Store("bad-plaintext")
	if _, err := w.Unwrap(context.Background(), wrapped, []byte("binding")); !errors.Is(err, ErrRemoteResponse) {
		t.Fatal(err)
	}
	f.mode.Store("slow")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := w.Wrap(ctx, key, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	before := f.requests.Load()
	cancel()
	if _, err := w.Wrap(ctx, key, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if f.requests.Load() != before {
		t.Fatal("expired call reached backend")
	}
}
