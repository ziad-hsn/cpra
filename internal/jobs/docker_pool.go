package jobs

import (
	"sync"

	"github.com/moby/moby/client"
)

// dockerClientPool stores shared Docker clients keyed by host.
// This prevents creating a new client for every intervention job.
var (
	dockerClientPool sync.Map // map[string]*client.Client
)

// GetDockerClient returns a shared *client.Client for the given host.
// If host is empty, it uses the default environment configuration.
// Clients are lazily initialized and cached.
func GetDockerClient(host string) (*client.Client, error) {
	// Normalize key for default environment
	key := host
	if key == "" {
		key = "default"
	}

	if v, ok := dockerClientPool.Load(key); ok {
		return v.(*client.Client), nil
	}

	var opts []client.Opt
	opts = append(opts, client.WithAPIVersionNegotiation())

	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}

	// Create new client
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}

	// Store in pool (LoadOrStore handles race conditions)
	actual, loaded := dockerClientPool.LoadOrStore(key, cli)
	if loaded {
		// Another goroutine created it first, close ours and use theirs
		_ = cli.Close()
		return actual.(*client.Client), nil
	}

	return cli, nil
}
