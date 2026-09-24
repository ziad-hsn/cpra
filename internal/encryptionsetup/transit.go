package encryptionsetup

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"time"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const initializationTimeout = 10 * time.Second
const maxSourceBytes = 1 << 20

func openTransit(ctx context.Context, source runtimeconfig.ManagementTransit, directory string) (*secureconfig.TransitWrapper, error) {
	policy := secureconfig.ProtectedFileOptions{Path: source.TokenFile, DataDirectory: directory, ReaderSID: source.ReaderSID, MaxBytes: 8194}
	if source.ReaderGroupID != nil {
		group := *source.ReaderGroupID
		policy.ReaderGroupID = &group
	}
	token := tokenSource(policy)
	// Verify the source before any backend request. Each request subsequently
	// reloads and rechecks the token, permitting protected token-file rotation.
	if _, err := token(ctx); err != nil {
		return nil, err
	}
	var client *http.Client
	if source.CAFile != "" {
		policy.Path = source.CAFile
		policy.MaxBytes = maxSourceBytes
		pool, err := certificatePool(ctx, policy)
		if err != nil {
			return nil, err
		}
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok || base == nil {
			return nil, ErrConfiguration
		}
		transport := base.Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, Timeout: initializationTimeout}
	}
	return secureconfig.NewTransitWrapper(ctx, secureconfig.TransitOptions{Address: source.Address, Mount: source.Mount, Key: source.Key, Namespace: source.Namespace, TokenSource: token, HTTPClient: client, Timeout: initializationTimeout})
}

func tokenSource(policy secureconfig.ProtectedFileOptions) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		data, err := secureconfig.ReadProtectedFile(ctx, policy)
		if err != nil {
			return "", err
		}
		defer clear(data)
		token := bytes.TrimSpace(data)
		if len(token) == 0 || len(token) > 8192 {
			return "", ErrConfiguration
		}
		for _, c := range token {
			if c < 0x21 || c > 0x7e {
				return "", ErrConfiguration
			}
		}
		return string(token), nil
	}
}

// CA certificates are public, but a modified trust root compromises wrapping.
// Apply the same explicit owner/service-reader integrity policy as token files.
func certificatePool(ctx context.Context, policy secureconfig.ProtectedFileOptions) (*x509.CertPool, error) {
	data, err := secureconfig.ReadProtectedFile(ctx, policy)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	pool := x509.NewCertPool()
	remaining := bytes.TrimSpace(data)
	count := 0
	for len(remaining) > 0 {
		// pem.Decode otherwise silently skips arbitrary text before a PEM block.
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, ErrConfiguration
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, ErrConfiguration
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, ErrConfiguration
		}
		pool.AddCert(certificate)
		count++
		remaining = bytes.TrimSpace(rest)
	}
	if count == 0 {
		return nil, ErrConfiguration
	}
	return pool, nil
}
