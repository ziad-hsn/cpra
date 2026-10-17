package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// checkTLS is immutable execution configuration. It carries no global provider
// registration; each check constructs its own effective transport policy.
type checkTLS struct {
	configured bool
	enabled    bool
	skip       bool
	caFile     string
	serverName string
}

func prepareCheckTLS(kind string, config any, enabled, skip *bool, caFile, serverName string) (checkTLS, error) {
	if err := validateCheckTLSSettings(kind, config); err != nil {
		return checkTLS{}, err
	}
	settings := checkTLS{configured: enabled != nil, caFile: caFile, serverName: serverName}
	if enabled != nil {
		settings.enabled = *enabled
	}
	if skip != nil {
		settings.skip = *skip
	}
	return settings, nil
}

func validateCheckTLSSettings(kind string, config any) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return errors.New("invalid check TLS settings")
	}
	normalized, err := api.NormalizeManifestDriver("check", kind, raw)
	if err != nil {
		return errors.New("invalid check TLS settings")
	}
	if err := api.ValidateTLSSettings(api.DriverConfig{Type: kind, Config: normalized}); err != nil {
		return err
	}
	return nil
}

func (settings checkTLS) configuration(host string) (*tls.Config, error) {
	if !settings.enabled {
		return nil, nil
	}
	name := settings.serverName
	if name == "" {
		name = host
	}
	return checkTLSConfiguration(settings.caFile, name, settings.skip)
}

func checkTLSConfiguration(caFile, serverName string, skip bool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, InsecureSkipVerify: skip}
	if caFile == "" {
		return config, nil
	}
	file, err := openCheckCertificate(caFile)
	if err != nil {
		return nil, errors.New("TLS CA file is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("TLS CA file must be regular and at most 1 MiB")
	}
	pem, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(pem) > 1<<20 {
		return nil, errors.New("TLS CA file cannot be read within its 1 MiB limit")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("TLS CA file contains no supported certificates")
	}
	config.RootCAs = roots
	return config, nil
}

func dialCheckTLS(ctx context.Context, network, address string, config *tls.Config) (net.Conn, error) {
	connection, err := dialBounded(ctx, network, address)
	if err != nil || config == nil {
		return connection, err
	}
	client := tls.Client(connection, config.Clone())
	if err := client.HandshakeContext(ctx); err != nil {
		connection.Close()
		return nil, err
	}
	return client, nil
}
