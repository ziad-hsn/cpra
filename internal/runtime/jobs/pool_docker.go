//go:build !nodocker

package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cpra/internal/platform/dockerhost"

	"github.com/moby/moby/client"
)

// Default Docker client with sync.Once for thread-safe lazy initialization.
// This is the most common case (empty host = use environment).
var (
	defaultDockerClient     *client.Client
	defaultDockerClientOnce sync.Once
	defaultDockerClientErr  error
)

// dockerClientEntry holds a client and its initialization state for custom hosts.
type dockerClientEntry struct {
	once   sync.Once
	client *client.Client
	err    error
}

// dockerClientPool stores shared Docker clients keyed by host.
// Each entry uses sync.Once to ensure exactly one initialization per host.
var dockerClientPool sync.Map // map[string]*dockerClientEntry

// validateContainerRef validates container references to prevent injection and traversal.
func validateContainerRef(containerRef string) error {
	if containerRef == "" {
		return fmt.Errorf("container reference cannot be empty")
	}
	if strings.Contains(containerRef, "..") ||
		strings.Contains(containerRef, "/") ||
		strings.Contains(containerRef, "\\") ||
		strings.Contains(containerRef, "$") ||
		strings.Contains(containerRef, "`") {
		return fmt.Errorf("invalid container reference: %s", containerRef)
	}
	if len(containerRef) > 200 {
		return fmt.Errorf("container reference too long: %d characters", len(containerRef))
	}
	return nil
}

// GetDockerClient returns a shared *client.Client for the given host.
// If host is empty, it uses the default environment configuration.
// Uses sync.Once to ensure each client is created exactly once (no duplicate creations).
func GetDockerClient(host string) (*client.Client, error) {
	// Fast path: default client (most common case)
	if host == "" {
		defaultDockerClientOnce.Do(func() {
			resolvedHost, err := dockerhost.Resolve("")
			if err != nil {
				defaultDockerClientErr = err
				return
			}

			opts := []client.Opt{
				client.FromEnv,
				client.WithAPIVersionNegotiation(),
				client.WithHost(resolvedHost),
			}

			// Enforce secure remote connections
			if dockerHost := os.Getenv("DOCKER_HOST"); dockerHost != "" {
				if strings.HasPrefix(dockerHost, "tcp://") && !strings.Contains(dockerHost, "tls") {
					defaultDockerClientErr = fmt.Errorf("insecure Docker connection not allowed: %s (use TLS)", dockerHost)
					return
				}
				if strings.Contains(dockerHost, ":") && !strings.HasPrefix(dockerHost, "unix://") {
					opts = append(opts, client.WithTLSClientConfig("", "", ""))
				}
			}

			defaultDockerClient, defaultDockerClientErr = client.NewClientWithOpts(opts...)

			// Validate API version compatibility
			if defaultDockerClientErr == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, versionErr := defaultDockerClient.ServerVersion(ctx); versionErr != nil {
					defaultDockerClientErr = fmt.Errorf("docker API version validation failed: %w", versionErr)
					defaultDockerClient = nil
				}
			}
		})
		return defaultDockerClient, defaultDockerClientErr
	}

	// Custom host: use per-host sync.Once via sync.Map
	entryI, _ := dockerClientPool.LoadOrStore(host, &dockerClientEntry{})
	entry := entryI.(*dockerClientEntry)

	entry.once.Do(func() {
		resolvedHost, err := dockerhost.Resolve(host)
		if err != nil {
			entry.err = err
			return
		}
		opts := []client.Opt{
			client.FromEnv,
			client.WithAPIVersionNegotiation(),
			client.WithHost(resolvedHost),
		}
		if strings.HasPrefix(host, "tcp://") && !strings.Contains(host, "tls") {
			entry.err = fmt.Errorf("insecure Docker connection not allowed: %s (use TLS)", host)
			return
		}
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "unix://") {
			opts = append(opts, client.WithTLSClientConfig("", "", ""))
		}

		entry.client, entry.err = client.NewClientWithOpts(opts...)
		if entry.err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, versionErr := entry.client.ServerVersion(ctx); versionErr != nil {
				entry.err = fmt.Errorf("docker API version validation failed: %w", versionErr)
				entry.client = nil
			}
		}
	})

	return entry.client, entry.err
}
