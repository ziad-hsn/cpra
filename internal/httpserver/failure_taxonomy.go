package httpserver

import (
	"slices"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func failureTaxonomies() []api.FailureTaxonomy {
	definitions, _ := api.FailureCodes(api.FailureTaxonomyVersion)
	available := map[string]bool{}
	for _, capability := range jobs.Capabilities() {
		if capability.Available && capability.Kind == "check" {
			available[capability.Driver] = true
		}
	}
	taxonomy := api.FailureTaxonomy{Version: api.FailureTaxonomyVersion, Codes: []api.FailureCodeInfo{}}
	for _, d := range definitions {
		drivers := slices.DeleteFunc(d.Drivers, func(driver string) bool { return !available[driver] })
		if len(drivers) == 0 {
			continue
		}
		details := d.Details
		if details == nil {
			details = []string{}
		}
		taxonomy.Codes = append(taxonomy.Codes, api.FailureCodeInfo{Code: d.Code, Message: d.Message, Available: d.Available, Eligible: d.Eligible, Phases: d.Phases, Details: details, Drivers: drivers})
	}
	return []api.FailureTaxonomy{taxonomy}
}
