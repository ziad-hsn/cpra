//go:build !externaljobs

package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type runtimePreparationExtensions struct{}

func (*RuntimePreparation) resolveRuntimeExtension(context.Context, api.Resource, persistence.CatalogRecord, string, string, string) (any, bool, error) {
	return nil, false, nil
}

func (p RuntimePreparation) configurationRevision(context.Context, map[persistence.CatalogKey]api.Resource, map[persistence.CatalogKey]persistence.CatalogRecord) (string, error) {
	return manifest.ConfigurationRevision(p.Monitor, p.Endpoints, p.NotificationGroups)
}
