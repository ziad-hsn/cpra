//go:build !nodocker

package jobs

import "testing"

func TestIsNilInterface_DockerJob(t *testing.T) {
	var job Job = (*InterventionDockerJob)(nil)
	if job == nil {
		t.Fatal("expected typed-nil interface, got nil")
	}
	if !job.IsNil() {
		t.Fatal("expected IsNil() to return true for typed-nil docker job")
	}
}

