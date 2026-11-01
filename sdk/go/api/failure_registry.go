package api

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
)

// FailureTaxonomyVersion identifies the immutable built-in failure vocabulary.
// A rule must name its version; a binary upgrade never expands an existing rule.
const FailureTaxonomyVersion = "1"

// FailureDefinition describes reviewed observations, not their root cause or
// permission to retry an external effect. Returned slices belong to the caller.
type FailureDefinition struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Phases    []string `json:"phases"`
	Drivers   []string `json:"drivers"`
	Details   []string `json:"details"`
	Eligible  bool     `json:"eligible"`
	Available bool     `json:"available"`
}

//go:embed failure_codes.json
var failureCodeBytes []byte

var failureRegistry = sync.OnceValue(func() map[string]FailureDefinition {
	var input struct {
		Version string                       `json:"version"`
		Codes   map[string]FailureDefinition `json:"codes"`
	}
	if json.Unmarshal(failureCodeBytes, &input) != nil || input.Version != FailureTaxonomyVersion {
		panic("invalid compiled failure taxonomy")
	}
	for code, definition := range input.Codes {
		definition.Code = code
		input.Codes[code] = definition
	}
	return input.Codes
})

// FailureCodes returns this version's vocabulary in stable code order.
func FailureCodes(version string) ([]FailureDefinition, error) {
	if version != FailureTaxonomyVersion {
		return nil, InvalidField("taxonomyVersion", "failureTaxonomy")
	}
	result := make([]FailureDefinition, 0, len(failureRegistry()))
	for _, definition := range failureRegistry() {
		result = append(result, cloneFailureDefinition(definition))
	}
	slices.SortFunc(result, func(a, b FailureDefinition) int {
		if a.Code < b.Code {
			return -1
		}
		if a.Code > b.Code {
			return 1
		}
		return 0
	})
	return result, nil
}

// FailureCodeDefinition looks up a code without changing its historical meaning.
func FailureCodeDefinition(version, code string) (FailureDefinition, bool) {
	if version != FailureTaxonomyVersion {
		return FailureDefinition{}, false
	}
	definition, ok := failureRegistry()[code]
	return cloneFailureDefinition(definition), ok
}

func cloneFailureDefinition(d FailureDefinition) FailureDefinition {
	d.Phases = slices.Clone(d.Phases)
	d.Drivers = slices.Clone(d.Drivers)
	d.Details = slices.Clone(d.Details)
	return d
}

// CanonicalFailure constructs a bounded observation without accepting remote
// message text. Protocol fields, when needed, must be supplied and validated.
func CanonicalFailure(version, code, phase string) (FailureDiagnostic, error) {
	d, ok := failureRegistry()[code]
	if version != FailureTaxonomyVersion || !ok || !slices.Contains(d.Phases, phase) {
		return FailureDiagnostic{}, InvalidField("code", "failureObservation")
	}
	return FailureDiagnostic{Code: code, Message: d.Message, Phase: phase, TaxonomyVersion: version}, nil
}
