//go:build externaljobs

package systems

import (
	"time"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func prepareCatalogMonitor(p management.RuntimePreparation, config runtimeconfig.Config) (*entities.PreparedMonitor, error) {
	if len(p.ExternalBindings) == 0 {
		return entities.PrepareMonitor(p.Monitor, p.Endpoints, p.NotificationGroups)
	}
	if !config.ExternalJobs.Enabled || !config.Management.Enabled || config.Storage.Mode != "raft" {
		return nil, errMonitorPreparation
	}
	return entities.PrepareMonitorExternal(p.Monitor, p.Endpoints, p.NotificationGroups, p.ExternalBindings)
}

// Preparation has already authenticated and frozen these source keys. Persist
// only the keys at their original notification positions; parameters and worker
// credential profiles remain private execution inputs.
func configureCatalogNotifications(policy *persistence.Policy, p management.RuntimePreparation) {
	for color, code := range p.Monitor.Codes {
		if !code.Dispatch {
			continue
		}
		var sources []persistence.WorkerNotificationSource
		if code.NotifyGroup == "" {
			if marker, ok := code.Config.(*manifest.ExternalNotificationConfig); ok && marker != nil {
				sources = []persistence.WorkerNotificationSource{notificationSource(marker.Key)}
			}
		} else {
			names := p.NotificationGroups[code.NotifyGroup]
			for ordinal, name := range names {
				marker, ok := p.Endpoints[name].Config.(*manifest.ExternalNotificationConfig)
				if !ok || marker == nil {
					continue
				}
				if sources == nil {
					sources = make([]persistence.WorkerNotificationSource, len(names))
				}
				sources[ordinal] = notificationSource(marker.Key)
			}
		}
		if len(sources) != 0 {
			if policy.WorkerNotificationSources == nil {
				policy.WorkerNotificationSources = make(map[string][]persistence.WorkerNotificationSource)
			}
			policy.WorkerNotificationSources[color] = sources
		}
	}
}

func notificationSource(key manifest.ExternalRuntimeKey) persistence.WorkerNotificationSource {
	return persistence.WorkerNotificationSource{Kind: key.SourceKind, ID: key.SourceID, UID: key.SourceUID, Revision: key.SourceRevision}
}

func localPulseInterval(storage *components.JobStorage, interval time.Duration) time.Duration {
	if storage != nil && storage.ExternalJobs != nil && storage.ExternalJobs.Check != nil {
		return 0
	}
	return interval
}
