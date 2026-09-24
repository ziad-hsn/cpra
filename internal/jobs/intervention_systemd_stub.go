//go:build !linux || !systemd

package jobs

import (
	"fmt"
	"runtime"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newInterventionSystemdJob(t *manifest.InterventionTargetSystemd, retries int, entity ecs.Entity) (Job, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("systemd intervention is only supported on Linux")
	}
	return nil, fmt.Errorf("systemd intervention requires building with -tags=systemd")
}
