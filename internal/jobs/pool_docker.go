package jobs

import (
	"sync"

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

// GetDockerClient returns a shared *client.Client for the given host.
// If host is empty, it uses the default environment configuration.
// Uses sync.Once to ensure each client is created exactly once (no duplicate creations).
func GetDockerClient(host string) (*client.Client, error) {
	// Fast path: default client (most common case)
	if host == "" {
		defaultDockerClientOnce.Do(func() {
			defaultDockerClient, defaultDockerClientErr = client.NewClientWithOpts(
				client.FromEnv,
				client.WithAPIVersionNegotiation(),
			)
		})
		return defaultDockerClient, defaultDockerClientErr
	}

	// Custom host: use per-host sync.Once via sync.Map
	entryI, _ := dockerClientPool.LoadOrStore(host, &dockerClientEntry{})
	entry := entryI.(*dockerClientEntry)

	entry.once.Do(func() {
		entry.client, entry.err = client.NewClientWithOpts(
			client.WithHost(host),
			client.WithAPIVersionNegotiation(),
		)
	})

	return entry.client, entry.err
}
