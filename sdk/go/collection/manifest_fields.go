package collection

import (
	"encoding/json"
	"fmt"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (b *builder) currentManifestFields() bool {
	return b.normalizationProfile == FileNormalizationProfile || b.normalizationProfile == "" && !b.allowDestinationConflict
}

func (b *builder) manifestDriverFields(category string, kind, config any) error {
	if !b.currentManifestFields() {
		return nil
	}
	name, _ := kind.(string)
	// The ordinary decoder owns missing driver/config and type errors.
	if name == "" || config == nil {
		return nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return api.InvalidField("config", "invalidType")
	}
	return api.ValidateManifestDriverFields(category, name, raw, b.jsonInput)
}

func (b *builder) monitorDriverFields(m map[string]any) error {
	if !b.currentManifestFields() {
		return nil
	}
	if pulse, ok := m["pulse_check"].(map[string]any); ok {
		if err := b.manifestDriverFields("check", pulse["type"], pulse["config"]); err != nil {
			return api.PrefixValidationError("spec.check.driver", err)
		}
	}
	if recovery, ok := m["intervention"].(map[string]any); ok {
		if err := b.manifestDriverFields("recovery", recovery["action"], recovery["target"]); err != nil {
			return api.PrefixValidationError("spec.recovery.driver", err)
		}
		if rules, ok := recovery["rules"].([]any); ok {
			if len(rules) > api.MaxRecoveryRules {
				return api.InvalidField("spec.recovery.rules", "ruleLimit")
			}
			for i, value := range rules {
				if rule, ok := value.(map[string]any); ok {
					if err := b.manifestDriverFields("recovery", rule["action"], rule["target"]); err != nil {
						return api.PrefixValidationError(fmt.Sprintf("spec.recovery.rules[%d].driver", i), err)
					}
				}
			}
		}
	}
	if codes, ok := m["codes"].(map[string]any); ok {
		for _, color := range []string{"blue", "cyan", "gray", "green", "red", "yellow"} {
			if code, ok := codes[color].(map[string]any); ok {
				if err := b.manifestDriverFields("notification", code["notify"], code["config"]); err != nil {
					return api.PrefixValidationError("spec.notifications."+color+".driver", err)
				}
			}
		}
	}
	return nil
}
