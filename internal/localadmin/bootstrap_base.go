//go:build !externaljobs

package localadmin

import (
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func bootstrapCatalogOptions(*management.CatalogOptions, runtimeconfig.Config) {}
