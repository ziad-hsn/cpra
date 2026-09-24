//go:build !teams

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newCodeTeamsJob(cfg *manifest.CodeNotificationTeams, monitor, color, message string, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("teams notification requires building with -tags=teams")
}
