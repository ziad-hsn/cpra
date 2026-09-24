package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

// initializeRuntimeAuthentication runs before creating an owner or listener.
// It never reads old credential sources once the authority has been committed.
func initializeRuntimeAuthentication(ctx context.Context, store *persistence.Store, settings runtimeconfig.Config, options runOptions) (persistence.AuthenticationState, *httpauth.Authorizer, error) {
	state, err := store.Authentication()
	if err != nil {
		return state, nil, err
	}
	if state.Version == 0 {
		command, err := loadAuthenticationBootstrap(ctx, settings, options)
		if err != nil {
			return state, nil, err
		}
		state, err = store.CommitAuthentication(ctx, command)
		if err != nil {
			return state, nil, fmt.Errorf("authentication bootstrap: %w", err)
		}
	}
	if state.ResetRequired {
		return state, nil, persistence.ErrAuthenticationResetRequired
	}
	if settings.Management.Enabled {
		if state.AnonymousLoopback || len(state.Principals) == 0 && state.LegacyTokenSHA256 != "" {
			return state, nil, errors.New("committed authentication has no named management principals; provision them through stopped local administration")
		}
		var proxy *httpauth.ProxyConfig
		if settings.Management.TrustedProxy != nil {
			proxy = &httpauth.ProxyConfig{PublicOrigin: settings.Management.TrustedProxy.PublicOrigin}
		}
		auth, err := httpauth.FromAuthentication(state, proxy)
		return state, auth, err
	}
	if len(state.Principals) != 0 {
		return state, nil, errors.New("committed named management authority exists; enable management to preserve its transport and permissions")
	}
	if state.AnonymousLoopback && options.web && !loopbackAddress(options.webAddr) {
		return state, nil, errors.New("anonymous authentication is restricted to a loopback web listener")
	}
	return state, nil, nil
}

// validateAuthenticationInputs validates only supplied bootstrap sources. A
// deployment provisioned locally need not retain a bootstrap policy file.
func validateAuthenticationInputs(ctx context.Context, settings runtimeconfig.Config, options runOptions) error {
	if settings.Management.Enabled && settings.Management.PolicyFile == "" {
		// TLS and the listener policy were validated independently. Reuse only
		// the optional legacy-source validation without pretending to bootstrap.
		settings.Management.Enabled = false
		options.web = false
	}
	_, err := loadAuthenticationBootstrap(ctx, settings, options)
	return err
}

// loadAuthenticationBootstrap is also used by -validate. It reads explicitly
// configured local inputs but opens no durable state or provider backend.
func loadAuthenticationBootstrap(ctx context.Context, settings runtimeconfig.Config, options runOptions) (persistence.AuthenticationCommand, error) {
	var policy httpauth.Config
	if settings.Management.Enabled {
		if settings.Management.PolicyFile == "" {
			return persistence.AuthenticationCommand{}, errors.New("fresh management authentication requires policy_file or stopped local provisioning")
		}
		data, err := secureconfig.ReadProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: settings.Management.PolicyFile, DataDirectory: settings.Storage.Directory,
			ReaderGroupID: settings.Management.PolicyReaderGroupID, ReaderSID: settings.Management.PolicyReaderSID, MaxBytes: 1 << 20})
		if err != nil {
			return persistence.AuthenticationCommand{}, fmt.Errorf("management bootstrap policy source: %w", err)
		}
		policy, err = httpauth.ParsePolicy(data)
		clear(data)
		if err != nil {
			return persistence.AuthenticationCommand{}, err
		}
	}
	legacy := options.webAuth
	if options.webAuthFile != "" {
		data, err := readLegacyWebToken(options.webAuthFile)
		if err != nil {
			return persistence.AuthenticationCommand{}, err
		}
		legacy = strings.TrimSpace(string(data))
		clear(data)
		if legacy == "" {
			return persistence.AuthenticationCommand{}, errors.New("web auth file is empty")
		}
	}
	if legacy != "" {
		if len(legacy) > httpauth.MaxTokenBytes {
			return persistence.AuthenticationCommand{}, errors.New("legacy token exceeds authentication compatibility limit")
		}
		for _, char := range []byte(legacy) {
			if char < 33 || char > 126 {
				return persistence.AuthenticationCommand{}, errors.New("invalid legacy authentication token")
			}
		}
		digest := sha256.Sum256([]byte(legacy))
		policy.LegacyTokenSHA256 = hex.EncodeToString(digest[:])
	}
	if settings.Management.Enabled {
		if err := httpauth.ValidateConfig(policy); err != nil {
			return persistence.AuthenticationCommand{}, err
		}
	}
	anonymous := !settings.Management.Enabled && policy.LegacyTokenSHA256 == ""
	if anonymous && options.web && !loopbackAddress(options.webAddr) {
		return persistence.AuthenticationCommand{}, errors.New("a non-loopback web listener requires an authentication token")
	}
	command := persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-bootstrap", At: time.Now().UTC(),
		LegacyTokenSHA256: policy.LegacyTokenSHA256, AnonymousLoopback: anonymous}
	for _, entry := range policy.Principals {
		command.Principals = append(command.Principals, persistence.AuthenticationPrincipal{ID: entry.ID, Role: entry.Role, TokenSHA256: entry.TokenSHA256, Revoked: entry.Revoked, ExpiresAt: entry.ExpiresAt})
	}
	return command, nil
}
