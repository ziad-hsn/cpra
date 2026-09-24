package secureconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go/logging"
)

// KMSOptions selects one existing customer-managed symmetric key by immutable
// ARN. Aliases are rejected. HTTPClient is copied privately; its transport and
// trust roots remain caller-owned. No custom endpoint URL is accepted.
type KMSOptions struct {
	KeyARN     string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// KMSWrapper uses only DescribeKey, Encrypt and Decrypt. The caller resolves its
// credentials/profile/files before construction; no credential data is retained
// in durable envelopes. Only Region, Credentials and HTTPClient are inherited
// from aws.Config. Endpoint overrides, middleware, logging, telemetry and retry
// settings are not inherited by this private cryptographic client.
//
// The stable ID includes the resolved ARN, which is also supplied and checked
// on every encrypt/decrypt. Provider-internal rotation preserves this identity.
// Changing the ARN requires Open+Seal; no KEK-only rewrap is provided because the
// existing envelope authenticates KeyID in its data and wrapping AAD.
type KMSWrapper struct {
	id         string
	arn        string
	client     *kms.Client
	httpClient *http.Client
	timeout    time.Duration
}

var kmsResourceID = regexp.MustCompile(`^key/(?:[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}|mrk-[a-fA-F0-9]{32})$`)
var kmsRegion = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z0-9]+-[0-9]+$`)

func NewKMSWrapper(ctx context.Context, config aws.Config, opts KMSOptions) (*KMSWrapper, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	identity, err := arn.Parse(opts.KeyARN)
	if err != nil || identity.Service != "kms" || identity.Region != config.Region || !kmsRegion.MatchString(config.Region) || len(identity.AccountID) != 12 || !kmsResourceID.MatchString(identity.Resource) || len(opts.KeyARN) > maxIdentity-8 || config.Credentials == nil {
		return nil, ErrRemoteConfiguration
	}
	if identity.Partition != "aws" && identity.Partition != "aws-cn" && identity.Partition != "aws-us-gov" {
		return nil, ErrRemoteConfiguration
	}
	for _, c := range identity.AccountID {
		if c < '0' || c > '9' {
			return nil, ErrRemoteConfiguration
		}
	}
	if identity.Partition == "aws-cn" && !strings.HasPrefix(config.Region, "cn-") || identity.Partition == "aws-us-gov" && !strings.HasPrefix(config.Region, "us-gov-") || identity.Partition == "aws" && (strings.HasPrefix(config.Region, "cn-") || strings.HasPrefix(config.Region, "us-gov-")) {
		return nil, ErrRemoteConfiguration
	}
	if config.EndpointResolver != nil || config.EndpointResolverWithOptions != nil || config.BaseEndpoint != nil {
		return nil, ErrRemoteConfiguration
	}
	timeout, err := remoteTimeout(opts.Timeout)
	if err != nil {
		return nil, err
	}
	source := opts.HTTPClient
	if source == nil && config.HTTPClient != nil {
		switch original := config.HTTPClient.(type) {
		case *http.Client:
			source = original
		case *awshttp.BuildableClient:
			source = &http.Client{Transport: original.GetTransport(), Timeout: original.GetTimeout()}
		default:
			return nil, ErrRemoteConfiguration
		}
	}
	client, err := privateWrappingHTTPClient(source, timeout)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			client.CloseIdleConnections()
		}
	}()
	endpoint, err := kms.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, kms.EndpointParameters{Region: aws.String(config.Region)})
	if err != nil || endpoint.URI.Scheme != "https" || endpoint.URI.Host == "" {
		return nil, ErrRemoteConfiguration
	}
	guarded := &kmsOriginClient{client: client, host: endpoint.URI.Host}
	private := aws.Config{Region: config.Region, Credentials: config.Credentials, HTTPClient: guarded, Logger: logging.Nop{}, Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
	w := &KMSWrapper{id: "aws-kms:" + opts.KeyARN, arn: opts.KeyARN, timeout: timeout, client: kms.NewFromConfig(private), httpClient: client}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := w.client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(opts.KeyARN)})
	if err != nil {
		return nil, remoteOperationError(ctx, err)
	}
	if result == nil || result.KeyMetadata == nil {
		return nil, ErrRemoteResponse
	}
	m := result.KeyMetadata
	if aws.ToString(m.Arn) != opts.KeyARN || !m.Enabled || m.KeyState != types.KeyStateEnabled || m.KeyManager != types.KeyManagerTypeCustomer || m.KeySpec != types.KeySpecSymmetricDefault || m.KeyUsage != types.KeyUsageTypeEncryptDecrypt || !slices.Contains(m.EncryptionAlgorithms, types.EncryptionAlgorithmSpecSymmetricDefault) {
		return nil, ErrRemoteKeyUnsupported
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	initialized = true
	return w, nil
}

// CloseIdleConnections releases only this adapter's owned idle connections.
// Borrowed custom transports and active requests remain under caller control.
func (w *KMSWrapper) CloseIdleConnections() {
	if w != nil && w.httpClient != nil {
		w.httpClient.CloseIdleConnections()
	}
}

type kmsOriginClient struct {
	client *http.Client
	host   string
}

func (client *kmsOriginClient) Do(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.Scheme != "https" || request.URL.Host != client.host || request.URL.User != nil || request.URL.RawQuery != "" || request.URL.Fragment != "" {
		return nil, ErrRemoteConfiguration
	}
	return client.client.Do(request)
}

func (w *KMSWrapper) ID() string {
	if w == nil {
		return ""
	}
	return w.id
}
func (w *KMSWrapper) String() string   { return "KMSWrapper{configuration:redacted}" }
func (w *KMSWrapper) GoString() string { return w.String() }

func (w *KMSWrapper) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.client == nil || len(key) != keySize {
		return nil, ErrInvalidKey
	}
	if len(aad) > MaxWrappedKey {
		return nil, ErrInvalidBinding
	}
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	result, err := w.client.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(w.arn), Plaintext: key, EncryptionAlgorithm: types.EncryptionAlgorithmSpecSymmetricDefault, EncryptionContext: kmsBindingContext(aad)})
	if err != nil {
		return nil, remoteOperationError(ctx, err)
	}
	if result == nil || aws.ToString(result.KeyId) != w.arn || result.EncryptionAlgorithm != types.EncryptionAlgorithmSpecSymmetricDefault || len(result.CiphertextBlob) == 0 || len(result.CiphertextBlob) > 6144 {
		return nil, ErrRemoteResponse
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result.CiphertextBlob, nil
}

func (w *KMSWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if w == nil || w.client == nil {
		return nil, ErrInvalidKey
	}
	if len(wrapped) == 0 || len(wrapped) > 6144 {
		return nil, ErrInvalidEnvelope
	}
	if len(aad) > MaxWrappedKey {
		return nil, ErrInvalidBinding
	}
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	result, err := w.client.Decrypt(ctx, &kms.DecryptInput{KeyId: aws.String(w.arn), CiphertextBlob: wrapped, EncryptionAlgorithm: types.EncryptionAlgorithmSpecSymmetricDefault, EncryptionContext: kmsBindingContext(aad)})
	if err != nil {
		var invalid *types.InvalidCiphertextException
		var wrongKey *types.IncorrectKeyException
		if (errors.As(err, &invalid) || errors.As(err, &wrongKey)) && ctx.Err() == nil {
			return nil, ErrAuthentication
		}
		return nil, remoteOperationError(ctx, err)
	}
	if result == nil {
		return nil, ErrRemoteResponse
	}
	if aws.ToString(result.KeyId) != w.arn || result.EncryptionAlgorithm != types.EncryptionAlgorithmSpecSymmetricDefault || len(result.Plaintext) != keySize {
		clear(result.Plaintext)
		return nil, ErrRemoteResponse
	}
	if ctx.Err() != nil {
		clear(result.Plaintext)
		return nil, ctx.Err()
	}
	return result.Plaintext, nil
}

func kmsBindingContext(aad []byte) map[string]string {
	digest := sha256.Sum256(aad)
	// EncryptionContext is logged in CloudTrail. Record only a domain and digest,
	// never raw bindings, credentials, plaintext data keys or provider parameters.
	return map[string]string{"cpra:purpose": "configuration-wrap-v1", "cpra:binding-sha256": hex.EncodeToString(digest[:])}
}
