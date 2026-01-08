package dockerhost

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"cpra/internal/platform"
)

var (
	errInvalidScheme = fmt.Errorf("docker host must use unix://, npipe://, or tcp://")
)

// Resolve normalizes a docker host string using platform defaults and validates the scheme.
// If host is empty, DOCKER_HOST is consulted; otherwise per-OS defaults are used.
func Resolve(host string) (string, error) {
	if host == "" {
		if env := os.Getenv("DOCKER_HOST"); env != "" {
			host = env
		}
	}

	caps := platform.Detect()
	if host == "" {
		switch caps.DefaultDockerHostScheme {
		case "npipe":
			host = "npipe:////./pipe/docker_engine"
		default:
			host = "unix:///var/run/docker.sock"
		}
	}

	// If no scheme, prepend the platform default.
	if !strings.Contains(host, "://") {
		prefix := caps.DefaultDockerHostScheme
		if prefix == "" {
			prefix = "unix"
		}
		host = prefix + "://" + host
	}

	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("invalid docker host: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "unix", "npipe", "tcp", "http", "https":
		// Allow tcp/http/https; TLS is controlled by Docker env (FromEnv) or daemon config.
	default:
		return "", errInvalidScheme
	}

	// For Windows, encourage npipe if default; allow tcp when explicitly set.
	if runtime.GOOS == "windows" && scheme == "unix" {
		return "", fmt.Errorf("unix sockets are not supported on Windows docker host")
	}

	return host, nil
}
