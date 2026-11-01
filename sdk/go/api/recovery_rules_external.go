//go:build externaljobs

package api

import (
	"encoding/json"
	"slices"
	"strings"
)

// ExternalFailureContract is the immutable vocabulary of an authenticated
// JobType version. Scope is part of the compiled program identity.
type ExternalFailureContract struct {
	JobTypeID       string   `json:"jobTypeID"`
	JobTypeUID      string   `json:"jobTypeUID"`
	Version         string   `json:"version"`
	ProtocolVersion string   `json:"protocolVersion"`
	Codes           []string `json:"codes,omitempty"`
}

// CloneWorkerFailure detaches every optional observation detail before retention
// or validation. Callers retain ownership of the original report.
func CloneWorkerFailure(in *WorkerFailure) *WorkerFailure {
	if in == nil {
		return nil
	}
	out := *in
	if in.HTTPStatus != nil {
		v := *in.HTTPStatus
		out.HTTPStatus = &v
	}
	if in.GRPCCode != nil {
		v := *in.GRPCCode
		out.GRPCCode = &v
	}
	if in.GRPCStatus != nil {
		v := *in.GRPCStatus
		out.GRPCStatus = &v
	}
	if in.CertificateNotAfter != nil {
		v := *in.CertificateNotAfter
		out.CertificateNotAfter = &v
	}
	if in.CertificateDaysRemaining != nil {
		v := *in.CertificateDaysRemaining
		out.CertificateDaysRemaining = &v
	}
	return &out
}

func ValidCustomFailureCode(code string) bool {
	if len(code) > 64 || !strings.HasPrefix(code, "custom.") {
		return false
	}
	for _, segment := range strings.Split(code[7:], ".") {
		if len(segment) == 0 || segment[0] < 'a' || segment[0] > 'z' {
			return false
		}
		for _, r := range segment {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
	}
	return true
}

func ValidateJobTypeFailures(spec JobTypeSpec) error {
	if spec.ProtocolVersion != "1" && !(spec.ProtocolVersion == "2" && spec.Kind == "check") {
		return InvalidField("protocolVersion", "externalFailureProtocol")
	}
	if len(spec.FailureCodes) > 32 || len(spec.FailureCodes) > 0 && (spec.ProtocolVersion != "2" || spec.Kind != "check") {
		return InvalidField("failureCodes", "failureCode")
	}
	seen := map[string]bool{}
	for _, code := range spec.FailureCodes {
		if !ValidCustomFailureCode(code) || seen[code] {
			return InvalidField("failureCodes", "failureCode")
		}
		seen[code] = true
	}
	return nil
}

func (c ExternalFailureContract) definitions() (map[string]FailureDefinition, error) {
	if c.ProtocolVersion != "2" || c.JobTypeID == "" || c.JobTypeUID == "" || c.Version == "" || len(c.JobTypeID) > 256 || len(c.JobTypeUID) > 256 || len(c.Version) > 256 {
		return nil, InvalidField("rules", "externalFailureProtocol")
	}
	if err := ValidateJobTypeFailures(JobTypeSpec{Kind: "check", ProtocolVersion: c.ProtocolVersion, FailureCodes: c.Codes}); err != nil {
		return nil, err
	}
	return externalFailureDefinitions(c.Codes), nil
}

func externalFailureDefinitions(codes []string) map[string]FailureDefinition {
	defs := make(map[string]FailureDefinition, len(failureRegistry())+len(codes))
	for code, d := range failureRegistry() {
		d = cloneFailureDefinition(d)
		d.Drivers = []string{"external"}
		defs[code] = d
	}
	for _, code := range codes {
		defs[code] = FailureDefinition{Code: code, Message: "External check reported a declared failure", Phases: []string{"check"}, Drivers: []string{"external"}, Eligible: true, Available: true}
	}
	return defs
}

// CompileExternalRulePredicates requires the exact retained contract, never a
// caller-supplied list of codes alone. The server independently verifies its UID.
func CompileExternalRulePredicates(c ExternalFailureContract, version string, rules []RulePredicate) (*CompiledRules, []RuleWarning, error) {
	defs, err := c.definitions()
	if err != nil {
		return nil, nil, err
	}
	scope, _ := json.Marshal(c)
	return compileRulePredicates("external", "", version, rules, defs, string(scope), true)
}

func CanonicalWorkerFailure(c ExternalFailureContract, report WorkerFailure) (FailureDiagnostic, error) {
	defs, err := c.definitions()
	if err != nil {
		return FailureDiagnostic{}, err
	}
	d, ok := defs[report.Code]
	if !ok {
		return FailureDiagnostic{}, InvalidField("failure.code", "failureCode")
	}
	observation := FailureDiagnostic{TaxonomyVersion: FailureTaxonomyVersion, Code: report.Code, Message: d.Message, Phase: report.Phase, HTTPStatus: report.HTTPStatus, GRPCCode: report.GRPCCode, CertificateDaysRemaining: report.CertificateDaysRemaining, CertificateNotAfter: report.CertificateNotAfter}
	if report.GRPCStatus != nil {
		v := FailureDiagnosticGrpcStatus(*report.GRPCStatus)
		observation.GRPCStatus = &v
	}
	if _, _, valid := failureObservationRegistry("external", observation, defs); !valid {
		return FailureDiagnostic{}, InvalidField("failure", "failureObservation")
	}
	return observation, nil
}

// Shape validation can precede reference resolution. The final collection/API
// graph must compile against the authenticated JobType before activation.
func validateExternalRecoverySettings(check DriverConfig, recovery RecoverySpec) error {
	if recovery.Driver.Type != "" || len(recovery.Driver.Config) > 0 || recovery.Driver.CredentialRefs != nil {
		return InvalidField("driver", "recoveryChoice")
	}
	if err := ValidateDriverSettings("check", check); err != nil {
		return err
	}
	predicates := make([]RulePredicate, 0, len(recovery.Rules))
	var codes []string
	for _, r := range recovery.Rules {
		if r.None == (r.Driver != nil) {
			return InvalidField("rules", "ruleDecision")
		}
		if r.Driver != nil {
			if err := ValidateDriverSettings("recovery", *r.Driver); err != nil {
				return err
			}
		}
		predicates = append(predicates, RulePredicate{ID: r.ID, Match: r.Match, None: r.None})
		for _, pattern := range r.Match.Codes {
			if strings.HasPrefix(pattern, "custom.") {
				candidate := pattern
				if strings.HasSuffix(pattern, ".*") {
					candidate = strings.TrimSuffix(pattern, "*") + "sample"
				}
				if !ValidCustomFailureCode(candidate) {
					return InvalidField("rules", "failureCode")
				}
				if !slices.Contains(codes, candidate) {
					codes = append(codes, candidate)
				}
			}
		}
	}
	// Placeholder definitions establish syntax only. Prefix coverage and the
	// 32-code declaration limit require the actual immutable JobType contract.
	// A prefix placeholder may coincide with a real exact selector without
	// proving that the later prefix is unreachable.
	_, _, err := compileRulePredicates("external", "", recovery.TaxonomyVersion, predicates, externalFailureDefinitions(codes), "", false)
	return err
}
