//go:build !externaljobs

package systems

import (
	"time"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func prepareCatalogMonitor(p management.RuntimePreparation, _ runtimeconfig.Config) (*entities.PreparedMonitor, error) {
	prepared, err := entities.PrepareMonitor(p.Monitor, p.Endpoints, p.NotificationGroups)
	if err == nil {
		prepared.SetTargetSummary(p.CheckTargetSummary)
	}
	return prepared, err
}

func configureCatalogNotifications(*persistence.Policy, management.RuntimePreparation) {}

func localPulseInterval(_ *components.JobStorage, interval time.Duration) time.Duration {
	return interval
}
