//go:build externaljobs

package api

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestStructuredFailureContractScope(t *testing.T) {
	c := ExternalFailureContract{JobTypeID: "probe", JobTypeUID: "original", Version: "v1", ProtocolVersion: "2", Codes: []string{"custom.cache.unavailable"}}
	predicates := []RulePredicate{{ID: "restart", Match: FailureMatch{Codes: []string{"custom.cache.*"}}}}
	p, _, err := CompileExternalRulePredicates(c, "1", predicates)
	if err != nil {
		t.Fatal(err)
	}
	f, err := CanonicalWorkerFailure(c, WorkerFailure{Code: "custom.cache.unavailable", Phase: "check"})
	if err != nil || !p.Match(f).Intervention {
		t.Fatal(f, err)
	}
	for _, report := range []WorkerFailure{{Code: "custom.cache.unknown", Phase: "check"}, {Code: "custom.cache.unavailable", Phase: "local"}, {Code: "not.registered", Phase: "check"}} {
		if _, err := CanonicalWorkerFailure(c, report); err == nil {
			t.Fatal("accepted undeclared or invalid observation")
		}
	}
	c.JobTypeUID = "replacement"
	other, _, err := CompileExternalRulePredicates(c, "1", predicates)
	if err != nil || other.Digest() == p.Digest() {
		t.Fatal("JobType incarnation not bound")
	}
	c.ProtocolVersion = "1"
	if _, _, err := CompileExternalRulePredicates(c, "1", predicates); err == nil {
		t.Fatal("protocol downgrade")
	}
}

func TestStructuredFailureNoDataCannotAuthorize(t *testing.T) {
	c := ExternalFailureContract{JobTypeID: "probe", JobTypeUID: "original", Version: "v1", ProtocolVersion: "2"}
	for _, code := range []string{"operation_failed", "ca_file_missing", "cancelled"} {
		_, _, err := CompileExternalRulePredicates(c, "1", []RulePredicate{{ID: "unsafe", Match: FailureMatch{Codes: []string{code}}}})
		if err == nil {
			t.Fatal("ineligible code authorized", code)
		}
	}
}

func TestExternalShapeValidationDefersCoverageToDeclaredVocabulary(t *testing.T) {
	check := DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"probe","version":"v1","parameters":{}}`)}
	rules := []RecoveryRule{{ID: "sample", Match: FailureMatch{Codes: []string{"custom.cache.sample"}}, None: true}, {ID: "remaining", Match: FailureMatch{Codes: []string{"custom.cache.*"}}, None: true}}
	policy := RecoverySpec{TaxonomyVersion: "1", Rules: rules}
	if err := ValidateRecoverySettings(check, &policy); err != nil {
		t.Fatal("placeholder became reachability authority", err)
	}
	contract := ExternalFailureContract{JobTypeID: "probe", JobTypeUID: "uid", Version: "v1", ProtocolVersion: "2", Codes: []string{"custom.cache.sample", "custom.cache.full"}}
	_, warnings, err := CompileExternalRulePredicates(contract, "1", []RulePredicate{{ID: rules[0].ID, Match: rules[0].Match, None: true}, {ID: rules[1].ID, Match: rules[1].Match, None: true}})
	if err != nil || len(warnings) != 1 {
		t.Fatal("actual vocabulary rejected", err, warnings)
	}
	contract.Codes = contract.Codes[:1]
	if _, _, err = CompileExternalRulePredicates(contract, "1", []RulePredicate{{ID: rules[0].ID, Match: rules[0].Match, None: true}, {ID: rules[1].ID, Match: rules[1].Match, None: true}}); err == nil {
		t.Fatal("actual unreachable rule accepted")
	}
	// More than 32 syntactic placeholders can refer to fewer than 32 declared
	// codes. Only the resolved contract enforces declaration cardinality.
	var codes []string
	for i := range 17 {
		codes = append(codes, fmt.Sprintf("custom.c%d.sample", i), fmt.Sprintf("custom.c%d.*", i))
	}
	policy.Rules = []RecoveryRule{{ID: "selectors-a", Match: FailureMatch{Codes: codes[:17]}, None: true}, {ID: "selectors-b", Match: FailureMatch{Codes: codes[17:]}, None: true}}
	if err := ValidateRecoverySettings(check, &policy); err != nil {
		t.Fatal("placeholder count treated as declaration count", err)
	}
}
