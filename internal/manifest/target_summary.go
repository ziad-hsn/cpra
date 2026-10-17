package manifest

import (
	"encoding/json"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// TargetSummary applies the public configuration projection to a file monitor.
func (p Pulse) TargetSummary() string {
	raw, err := json.Marshal(p.Config)
	if err != nil {
		return ""
	}
	normalized, err := api.NormalizeManifestDriver("check", p.Type, raw)
	if err != nil {
		return ""
	}
	return api.CheckTargetSummary(api.DriverConfig{Type: p.Type, Config: normalized})
}
