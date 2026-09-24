package encryptionsetup

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/logging"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func openKMS(ctx context.Context, source runtimeconfig.ManagementAWSKMS, directory string) (*secureconfig.KMSWrapper, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, initializationTimeout)
	defer cancel()
	// Explicit files have the same strict external-to-state owner-only policy.
	// Default SDK credential sources remain the operator-selected official chain.
	source.SharedConfigFiles = slices.Clone(source.SharedConfigFiles)
	source.SharedCredentialsFiles = slices.Clone(source.SharedCredentialsFiles)
	paths := append(slices.Clone(source.SharedConfigFiles), source.SharedCredentialsFiles...)
	for _, path := range paths {
		contents, err := secureconfig.ReadProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: path, DataDirectory: directory, MaxBytes: maxSourceBytes})
		if err != nil {
			return nil, nil, err
		}
		clear(contents)
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		return nil, nil, ErrConfiguration
	}
	transport := base.Clone()
	client := &http.Client{Transport: &credentialTransport{base: transport}, Timeout: initializationTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cleanup := transport.CloseIdleConnections
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(source.Region), awsconfig.WithHTTPClient(client),
		awsconfig.WithLogger(logging.Nop{}), awsconfig.WithClientLogMode(0), awsconfig.WithLogConfigurationWarnings(false),
		awsconfig.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
	}
	if source.Profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(source.Profile))
	}
	if source.SharedConfigFiles != nil {
		options = append(options, awsconfig.WithSharedConfigFiles(source.SharedConfigFiles))
	}
	if source.SharedCredentialsFiles != nil {
		options = append(options, awsconfig.WithSharedCredentialsFiles(source.SharedCredentialsFiles))
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, ErrBackend
	}
	// Use a separate standard client for wrapping; the adapter privately clones it.
	// The SDK credential provider retains its own bounded, nonredirecting client.
	wrapper, err := secureconfig.NewKMSWrapper(ctx, configuration, secureconfig.KMSOptions{KeyARN: source.KeyARN, HTTPClient: &http.Client{Transport: transport, Timeout: initializationTimeout}, Timeout: initializationTimeout})
	if err != nil {
		return nil, nil, err
	}
	success = true
	return wrapper, cleanup, nil
}

// Credential acquisition also bounds network response bodies. Use the official
// provider chain, including operator-selected profile helpers; do not substitute
// provider credentials into CPRa resource configuration or errors.
type credentialTransport struct{ base http.RoundTripper }

func (t *credentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, ErrBackend
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil {
		clear(body)
		return nil, err
	}
	if len(body) > 256<<10 {
		clear(body)
		return nil, ErrBackend
	}
	response.Body = &credentialBody{Reader: bytes.NewReader(body), data: body}
	return response, nil
}

type credentialBody struct {
	*bytes.Reader
	data []byte
}

func (b *credentialBody) Close() error { clear(b.data); b.data = nil; return nil }
