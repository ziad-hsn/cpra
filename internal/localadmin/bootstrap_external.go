//go:build externaljobs

package localadmin

import (
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func bootstrapCatalogOptions(options *management.CatalogOptions, cfg runtimeconfig.Config) {
	options.ExternalJobsEnabled = cfg.ExternalJobs.Enabled
}
