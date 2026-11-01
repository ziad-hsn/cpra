package api

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func rulesHTTPCheck() DriverConfig {
	return DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.invalid/health"}`)}
}
func rulesRecoveryDriver() *DriverConfig {
	return &DriverConfig{Type: "webhook", Config: json.RawMessage(`{"url":"https://example.invalid/recover"}`)}
}
func testRecoveryRule(id string, codes ...string) RecoveryRule {
	return RecoveryRule{ID: id, Match: FailureMatch{Codes: codes}, Driver: rulesRecoveryDriver()}
}
func testRecoveryPolicy(rules ...RecoveryRule) RecoverySpec {
	return RecoverySpec{TaxonomyVersion: "1", Rules: rules}
}
func rulesFailure(t *testing.T, code, phase string) FailureDiagnostic {
	t.Helper()
	f, err := CanonicalFailure("1", code, phase)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRecoveryRulesFirstMatchAndDefault(t *testing.T) {
	ignore := testRecoveryRule("ignore-401", "unexpected_http_status")
	ignore.Driver, ignore.None, ignore.Match.HTTPStatuses = nil, true, []int{401}
	policy, warnings, err := CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(ignore, testRecoveryRule("http-failure", "unexpected_http_status")))
	if err != nil || len(warnings) != 1 || warnings[0].Reason != "partialOverlap" {
		t.Fatalf("compile: %v, %v", err, warnings)
	}
	for _, tc := range []struct {
		status    int
		id        string
		intervene bool
	}{{401, "ignore-401", false}, {503, "http-failure", true}} {
		failure := rulesFailure(t, "unexpected_http_status", "response")
		failure.HTTPStatus = &tc.status
		decision := policy.Match(failure)
		if decision.RuleID != tc.id || decision.Intervention != tc.intervene || !decision.Matched {
			t.Fatalf("status %d: %+v", tc.status, decision)
		}
	}
	if decision := policy.Match(rulesFailure(t, "network.connection_refused", "connect")); decision.Matched || decision.Intervention || decision.Index != -1 {
		t.Fatalf("default: %+v", decision)
	}
}

func TestRecoveryRuleValidation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*RecoverySpec)
	}{
		{"no version", "failureTaxonomy", func(p *RecoverySpec) { p.TaxonomyVersion = "" }},
		{"future version", "failureTaxonomy", func(p *RecoverySpec) { p.TaxonomyVersion = "2" }},
		{"both action and rules", "recoveryChoice", func(p *RecoverySpec) { p.Driver = *rulesRecoveryDriver() }},
		{"duplicate ids", "ruleIdentity", func(p *RecoverySpec) { p.Rules = append(p.Rules, p.Rules[0]) }},
		{"invalid id", "ruleIdentity", func(p *RecoverySpec) { p.Rules[0].ID = "../secret" }},
		{"both decisions", "ruleDecision", func(p *RecoverySpec) { p.Rules[0].None = true }},
		{"missing decision", "ruleDecision", func(p *RecoverySpec) { p.Rules[0].Driver = nil }},
		{"unknown code", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"private-secret"} }},
		{"regexp", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"tls.*.expired"} }},
		{"bare wildcard", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"*"} }},
		{"unclassified", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"operation_failed"} }},
		{"local failure", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"ca_file_invalid"} }},
		{"driver mismatch", "failureCode", func(p *RecoverySpec) { p.Rules[0].Match.Codes = []string{"docker.not_running"} }},
		{"phase mismatch", "failureMatcher", func(p *RecoverySpec) { p.Rules[0].Match.Phases = []string{"local"} }},
		{"protocol mismatch", "failureMatcher", func(p *RecoverySpec) { p.Rules[0].Match.HTTPStatuses = []int{503} }},
		{"unknown status", "failureMatcher", func(p *RecoverySpec) { p.Rules[0].Match.GRPCCodes = []string{"SECRET"} }},
		{"max rules", "ruleLimit", func(p *RecoverySpec) { p.Rules = make([]RecoveryRule, 33) }},
		{"unreachable", "unreachableRule", func(p *RecoverySpec) {
			p.Rules = append(p.Rules, testRecoveryRule("later", "network.connection_refused"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testRecoveryPolicy(testRecoveryRule("recover", "network.connection_refused"))
			tc.change(&p)
			_, _, err := CompileRecoveryRules(rulesHTTPCheck(), p)
			var detail *ValidationError
			if !errors.As(err, &detail) || detail.Reason != tc.reason {
				t.Fatalf("got %v, want %s", err, tc.reason)
			}
			if strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "../secret") {
				t.Fatal("rejected value leaked")
			}
		})
	}
}

func TestRecoveryRulesUnionCoverage(t *testing.T) {
	a, b, c := testRecoveryRule("one", "unexpected_http_status"), testRecoveryRule("two", "unexpected_http_status"), testRecoveryRule("three", "unexpected_http_status")
	a.Match.HTTPStatuses, b.Match.HTTPStatuses, c.Match.HTTPStatuses = []int{500}, []int{503}, []int{500, 503}
	_, _, err := CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(a, b, c))
	var detail *ValidationError
	if !errors.As(err, &detail) || detail.Reason != "unreachableRule" {
		t.Fatalf("union coverage: %v", err)
	}
	c.Match.HTTPStatuses = append(c.Match.HTTPStatuses, 504)
	_, warnings, err := CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(a, b, c))
	if err != nil || len(warnings) != 1 || warnings[0].RuleID != "three" {
		t.Fatalf("partial union: %v %v", warnings, err)
	}
}

func TestRecoveryRulesPrefixIsolationAndImmutability(t *testing.T) {
	spec := testRecoveryPolicy(testRecoveryRule("certificate", "tls.cert.*"))
	policy, _, err := CompileRecoveryRules(rulesHTTPCheck(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Rules[0].ID = "changed"
	spec.Rules[0].Match.Codes[0] = "network.connection_refused"
	failure := rulesFailure(t, "tls.cert.untrusted", "tls")
	if got := policy.Match(failure); got.RuleID != "certificate" || !got.Intervention {
		t.Fatalf("mutated compilation: %+v", got)
	}
	failure.Code = "tls.cert.future"
	if policy.Match(failure).Matched {
		t.Fatal("unknown code widened prefix")
	}
	failure = rulesFailure(t, "tls.cert.untrusted", "tls")
	failure.TaxonomyVersion = "2"
	if policy.Match(failure).Matched {
		t.Fatal("different taxonomy accepted")
	}
	definition, _ := FailureCodeDefinition("1", "tls.cert.untrusted")
	definition.Drivers[0] = "changed"
	copy, _ := FailureCodeDefinition("1", "tls.cert.untrusted")
	if copy.Drivers[0] == "changed" {
		t.Fatal("registry slices escaped")
	}
}

func TestRecoveryRulesProtocolDetailsAndUnavailable(t *testing.T) {
	check := DriverConfig{Type: "grpc", Config: json.RawMessage(`{"host":"localhost","port":443,"mode":"health"}`)}
	rule := testRecoveryRule("health", "grpc_health_failed")
	rule.Match.GRPCStatuses = []FailureMatchGrpcStatuses{"NOT_SERVING"}
	p, _, err := CompileRecoveryRules(check, testRecoveryPolicy(rule))
	if err != nil {
		t.Fatal(err)
	}
	f := rulesFailure(t, "grpc_health_failed", "response")
	status := FailureDiagnosticGrpcStatus("NOT_SERVING")
	f.GRPCStatus = &status
	if !p.Match(f).Intervention {
		t.Fatal("health status did not match")
	}
	rpc := "Unavailable"
	f.GRPCCode = &rpc
	if p.Match(f).Matched {
		t.Fatal("contradictory health/transport report accepted")
	}
	check.Config = json.RawMessage(`{"host":"localhost","port":443}`)
	if _, _, err := CompileRecoveryRules(check, testRecoveryPolicy(rule)); err == nil {
		t.Fatal("health rule accepted for TCP mode")
	}
	none := RecoveryRule{ID: "unavailable", None: true, Match: FailureMatch{Codes: []string{"ca_file_invalid"}}}
	p, _, err = CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(none))
	if err != nil {
		t.Fatal(err)
	}
	decision := p.Match(rulesFailure(t, "ca_file_invalid", "local"))
	if !decision.Matched || decision.Eligible || decision.Intervention {
		t.Fatalf("unavailable authorized: %+v", decision)
	}
}

func TestRecoveryRulesConcurrentAndBounded(t *testing.T) {
	p, _, err := CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(testRecoveryRule("recover", "network.connection_refused")))
	if err != nil {
		t.Fatal(err)
	}
	f := rulesFailure(t, "network.connection_refused", "connect")
	if allocs := testing.AllocsPerRun(100, func() {
		if !p.Match(f).Intervention {
			panic("missing match")
		}
	}); allocs != 0 {
		t.Fatalf("matching allocations: %g", allocs)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				if !p.Match(f).Intervention {
					t.Error("concurrent mismatch")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestRecoveryRulesWireShape(t *testing.T) {
	raw, err := json.Marshal(testRecoveryPolicy(testRecoveryRule("recover", "network.connection_refused")))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if _, exists := object["driver"]; exists {
		t.Fatalf("rules emitted empty singleton driver: %s", raw)
	}
	var decoded RecoverySpec
	if err = StrictDecode(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Rules) != 1 || decoded.Rules[0].ID != "recover" {
		t.Fatalf("round trip: %+v", decoded)
	}
}

func TestRecoveryRulesRejectEmptyMatcherArrays(t *testing.T) {
	for _, field := range []string{"phases", "httpStatuses", "grpcCodes", "grpcStatuses"} {
		t.Run(field, func(t *testing.T) {
			var policy RecoverySpec
			input := `{"taxonomyVersion":"1","rules":[{"id":"one","match":{"codes":["unexpected_http_status"],"` + field + `":[]},"driver":{"type":"webhook","config":{"url":"https://example.invalid/recover"}}}]}`
			if err := StrictDecode([]byte(input), &policy); err != nil {
				t.Fatal(err)
			}
			if _, _, err := CompileRecoveryRules(rulesHTTPCheck(), policy); err == nil {
				t.Fatal("empty restriction broadened the matcher")
			}
		})
	}
}

func TestRecoveryRulesRejectNullRestrictions(t *testing.T) {
	for _, field := range []string{"codes", "phases", "httpStatuses", "grpcCodes", "grpcStatuses"} {
		var match FailureMatch
		if err := StrictDecode([]byte(`{"`+field+`":null}`), &match); err == nil {
			t.Errorf("null %s accepted", field)
		}
	}
	for _, input := range []string{`{"rules":null}`, `{"taxonomyVersion":null}`, `{"rules":[],"driver":null}`} {
		var policy RecoverySpec
		if err := StrictDecode([]byte(input), &policy); err == nil {
			t.Errorf("ambiguous rule input accepted: %s", input)
		}
	}
	for _, input := range []string{`{"codes":["unexpected_http_status"],"HTTPStatuses":null}`, `{"codes":["unexpected_http_status"],"httpStatuses":[503],"HttpStatuses":null}`} {
		var match FailureMatch
		if err := StrictDecode([]byte(input), &match); err == nil {
			t.Error("case alias bypassed matcher constraints")
		}
	}
}

func TestTypedRecoveryRulesDoNotDropEmptyRestrictions(t *testing.T) {
	for _, mutate := range []func(*RecoverySpec){
		func(p *RecoverySpec) { p.Rules[0].Match.HTTPStatuses = []int{} },
		func(p *RecoverySpec) { p.Rules[0].Match.Phases = []string{} },
		func(p *RecoverySpec) { p.Driver = *rulesRecoveryDriver(); p.Rules = []RecoveryRule{} },
	} {
		policy := testRecoveryPolicy(testRecoveryRule("one", "unexpected_http_status"))
		mutate(&policy)
		spec := MonitorSpec{Check: CheckSpec{Driver: rulesHTTPCheck(), Interval: "60s", Timeout: "5s"}, Recovery: &policy}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		resource := Resource{APIVersion: APIVersion, Kind: "Monitor", Metadata: Metadata{ID: "service"}, Spec: raw}
		if err := ValidateResourceValue(resource); err == nil {
			t.Fatal("typed empty restriction became a broader mutation")
		}
	}
}
