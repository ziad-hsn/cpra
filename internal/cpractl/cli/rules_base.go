//go:build !externaljobs

package cli

import (
	"context"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func offlineCatalogOptions() management.CatalogOptions { return management.CatalogOptions{} }

func compileOfflineRules(_ context.Context, _ *collection.Frozen, spec api.MonitorSpec, details api.FailureDiagnostic) (*api.CompiledRules, []api.RuleWarning, api.FailureDiagnostic, error) {
	return compileOfflineBuiltinRules(spec, details)
}
