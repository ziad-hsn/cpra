//go:build nodocker

package jobs

import "errors"

// ErrDockerDisabled is returned when Docker support is disabled via build tags.
var ErrDockerDisabled = errors.New("docker support disabled (build with -tags nodocker)")

// GetDockerClient returns an error when Docker is disabled.
func GetDockerClient(host string) (interface{}, error) {
	return nil, ErrDockerDisabled
}





