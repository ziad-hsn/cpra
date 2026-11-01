package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unsafe"
)

const MaxRecoveryRules = 32

// VisitRecoveryDrivers visits every configured action branch in source order.
// Paths are relative to recovery and contain only canonical property names and
// numeric indices. The visitor may replace a driver while protecting or resolving
// its configuration. It must not invoke an external operation.
func VisitRecoveryDrivers(recovery *RecoverySpec, visit func(string, *DriverConfig) error) error {
	if recovery == nil {
		return nil
	}
	if len(recovery.Rules) == 0 {
		return visit("driver", &recovery.Driver)
	}
	for i := range recovery.Rules {
		if driver := recovery.Rules[i].Driver; driver != nil {
			if err := visit(fmt.Sprintf("rules[%d].driver", i), driver); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateRecoverySettings validates the complete selection contract without
// resolving a credential, opening storage, reading CA files, or creating clients.
func ValidateRecoverySettings(check DriverConfig, recovery *RecoverySpec) error {
	if recovery == nil {
		return nil
	}
	if recovery.Rules != nil || recovery.TaxonomyVersion != "" {
		if check.Type == "external" {
			return validateExternalRecoverySettings(check, *recovery)
		}
		_, _, err := CompileRecoveryRules(check, *recovery)
		return err
	}
	return PrefixValidationError("driver", ValidateDriverSettings("recovery", recovery.Driver))
}

var failurePhases = []string{"check", "local", "resolve", "connect", "tls", "request", "response", "authentication", "authorization"}
var failureGRPCCodes = []string{"Canceled", "Unknown", "InvalidArgument", "DeadlineExceeded", "NotFound", "AlreadyExists", "PermissionDenied", "ResourceExhausted", "FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented", "Internal", "Unavailable", "DataLoss", "Unauthenticated"}
var failureGRPCStatuses = []string{"UNKNOWN", "NOT_SERVING", "SERVICE_UNKNOWN"}

// RuleWarning identifies partial overlap without including configuration values.
type RuleWarning struct {
	RuleID string `json:"ruleID"`
	Reason string `json:"reason"`
}

// RuleDecision is a match explanation. It is not permission to execute; the
// controller must qualify observations and commit an authorized action start.
type RuleDecision struct {
	RuleID       string `json:"ruleID,omitempty"`
	Index        int    `json:"index"`
	Matched      bool   `json:"matched"`
	Intervention bool   `json:"intervention"`
	Eligible     bool   `json:"eligible"`
}

// CompiledRules is immutable and safe for concurrent matching. It contains no
// provider configuration or credentials. Match performs no I/O or allocation.
type CompiledRules struct {
	version     string
	driver      string
	digest      string
	rules       []compiledRule
	definitions map[string]FailureDefinition
}

type compiledRule struct {
	id      string
	none    bool
	clauses []failureClause
}

// Each finite domain has at most 500 HTTP statuses, or 19 gRPC observations.
// Representing them as bits permits exact union coverage without constructing a
// Cartesian product or guessing that one partially overlapping rule shadows another.
type failureValues [8]uint64
type failureClause struct {
	code   string
	phases uint16
	values failureValues
}

func (v *failureValues) add(index int) { v[index/64] |= uint64(1) << (index % 64) }
func (v failureValues) has(index int) bool {
	return index >= 0 && index < 512 && v[index/64]&(uint64(1)<<(index%64)) != 0
}
func (v failureValues) empty() bool { return v == (failureValues{}) }

// Digest identifies only the compiled matching policy, including rule order and
// taxonomy. It deliberately excludes action settings; action ownership additionally
// requires the committed configuration revision and dependency identities.
func (p *CompiledRules) Digest() string { return p.digest }

// MemoryBytes accounts for the immutable program's value, slice backing arrays
// and string payloads. It excludes allocator rounding and shared registry data.
func (p *CompiledRules) MemoryBytes() uint64 {
	if p == nil {
		return 0
	}
	n := uint64(unsafe.Sizeof(*p)) + uint64(len(p.version)+len(p.driver)+len(p.digest))
	n += uint64(cap(p.rules)) * uint64(unsafe.Sizeof(compiledRule{}))
	for _, r := range p.rules {
		n += uint64(len(r.id)) + uint64(cap(r.clauses))*uint64(unsafe.Sizeof(failureClause{}))
		for _, c := range r.clauses {
			n += uint64(len(c.code))
		}
	}
	if p.driver == "external" {
		// External programs own their immutable declaration map. Charge a
		// conservative map overhead plus every slice/string payload; built-in
		// programs borrow the process registry instead.
		for code, d := range p.definitions {
			n += 512 + uint64(len(code)+len(d.Code)+len(d.Message))
			for _, values := range [][]string{d.Phases, d.Drivers, d.Details} {
				n += uint64(cap(values)) * uint64(unsafe.Sizeof(""))
				for _, v := range values {
					n += uint64(len(v))
				}
			}
		}
	}
	return n
}

// CompileRecoveryRules validates and freezes an ordered built-in check policy.
// Every action branch is statically validated, even when another rule shadows it.
// External checks use their immutable, authenticated JobType contract separately.
func CompileRecoveryRules(check DriverConfig, recovery RecoverySpec) (*CompiledRules, []RuleWarning, error) {
	if check.Type == "external" {
		return nil, nil, InvalidField("rules", "externalFailureProtocol")
	}
	if recovery.TaxonomyVersion != FailureTaxonomyVersion {
		return nil, nil, InvalidField("taxonomyVersion", "failureTaxonomy")
	}
	if recovery.Driver.Type != "" || len(recovery.Driver.Config) != 0 || recovery.Driver.CredentialRefs != nil {
		return nil, nil, InvalidField("driver", "recoveryChoice")
	}
	if len(recovery.Rules) == 0 || len(recovery.Rules) > MaxRecoveryRules {
		return nil, nil, InvalidField("rules", "ruleLimit")
	}
	if err := ValidateDriver("check", check); err != nil {
		return nil, nil, err
	}
	rules := make([]RulePredicate, len(recovery.Rules))
	for index, rule := range recovery.Rules {
		field := fmt.Sprintf("rules[%d]", index)
		if rule.None == (rule.Driver != nil) {
			return nil, nil, InvalidField(field, "ruleDecision")
		}
		if rule.Driver != nil {
			if err := ValidateDriverSettings("recovery", *rule.Driver); err != nil {
				return nil, nil, PrefixValidationError(field+".driver", err)
			}
		}
		rules[index] = RulePredicate{ID: rule.ID, Match: rule.Match, None: rule.None}
	}
	mode := ""
	if check.Type == "grpc" {
		var value PulseGRPCConfig
		_ = json.Unmarshal(check.Config, &value)
		if value.Mode != nil {
			mode = string(*value.Mode)
		}
	}
	return CompileRulePredicates(check.Type, mode, recovery.TaxonomyVersion, rules)
}

// RulePredicate is the credential-free matching input retained with an action
// policy. Action settings and dependency identities remain separate authorities.
type RulePredicate struct {
	ID    string       `json:"id"`
	Match FailureMatch `json:"match"`
	None  bool         `json:"none,omitempty"`
}

// CompileRulePredicates freezes only decision inputs. It grants no execution
// permission and does not replace validation of the complete recovery resource.
func CompileRulePredicates(driver, mode, version string, rules []RulePredicate) (*CompiledRules, []RuleWarning, error) {
	if driver == "external" {
		return nil, nil, InvalidField("rules", "externalFailureProtocol")
	}
	return compileRulePredicates(driver, mode, version, rules, failureRegistry(), "", true)
}

func compileRulePredicates(driver, mode, version string, rules []RulePredicate, definitions map[string]FailureDefinition, scope string, checkCoverage bool) (*CompiledRules, []RuleWarning, error) {
	if version != FailureTaxonomyVersion {
		return nil, nil, InvalidField("taxonomyVersion", "failureTaxonomy")
	}
	if len(rules) == 0 || len(rules) > MaxRecoveryRules {
		return nil, nil, InvalidField("rules", "ruleLimit")
	}
	check := DriverConfig{Type: driver}
	check.Config, _ = json.Marshal(struct {
		Mode string `json:"mode,omitempty"`
	}{mode})
	policy := &CompiledRules{version: version, driver: check.Type, definitions: definitions}
	seen := map[string]bool{}
	var warnings []RuleWarning
	for i, rule := range rules {
		field := fmt.Sprintf("rules[%d]", i)
		if !validRuleID(rule.ID) || seen[rule.ID] {
			return nil, nil, InvalidField(field+".id", "ruleIdentity")
		}
		seen[rule.ID] = true
		clauses, err := compileFailureMatchRegistry(check, rule.Match, rule.None, definitions)
		if err != nil {
			return nil, nil, PrefixValidationError(field+".match", err)
		}
		overlap, covered := false, false
		if checkCoverage {
			overlap, covered = ruleCoverage(clauses, policy.rules)
		}
		if covered {
			return nil, nil, InvalidField(field, "unreachableRule")
		}
		if overlap {
			warnings = append(warnings, RuleWarning{RuleID: rule.ID, Reason: "partialOverlap"})
		}
		policy.rules = append(policy.rules, compiledRule{id: rule.ID, none: rule.None, clauses: clauses})
	}
	// Hash a canonical, credential-free representation rather than caller-owned slices.
	canonical := struct {
		Version, Driver string
		Scope           string
		Rules           []any
	}{Version: policy.version, Driver: policy.driver, Scope: scope}
	for _, r := range policy.rules {
		clauses := make([]any, 0, len(r.clauses))
		for _, c := range r.clauses {
			clauses = append(clauses, []any{c.code, c.phases, c.values})
		}
		canonical.Rules = append(canonical.Rules, []any{r.id, r.none, clauses})
	}
	data, _ := json.Marshal(canonical)
	digest := sha256.Sum256(data)
	policy.digest = hex.EncodeToString(digest[:])
	return policy, warnings, nil
}

func validRuleID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i, c := range []byte(id) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

func compileFailureMatch(check DriverConfig, match FailureMatch, none bool) ([]failureClause, error) {
	return compileFailureMatchRegistry(check, match, none, failureRegistry())
}
func compileFailureMatchRegistry(check DriverConfig, match FailureMatch, none bool, definitions map[string]FailureDefinition) ([]failureClause, error) {
	if len(match.Codes) == 0 || len(match.Codes) > 32 {
		return nil, InvalidField("codes", "failureCode")
	}
	if len(match.Phases) > 8 || len(match.HTTPStatuses) > 100 || len(match.GRPCCodes) > 16 || len(match.GRPCStatuses) > 3 {
		return nil, InvalidField("", "failureMatcher")
	}
	if match.Phases != nil && len(match.Phases) == 0 || match.HTTPStatuses != nil && len(match.HTTPStatuses) == 0 || match.GRPCCodes != nil && len(match.GRPCCodes) == 0 || match.GRPCStatuses != nil && len(match.GRPCStatuses) == 0 {
		return nil, InvalidField("", "failureMatcher")
	}
	if !none && slices.Contains(match.GRPCCodes, "Canceled") {
		return nil, InvalidField("grpcCodes", "failureMatcher")
	}
	detailKinds := 0
	for _, count := range []int{len(match.HTTPStatuses), len(match.GRPCCodes), len(match.GRPCStatuses)} {
		if count > 0 {
			detailKinds++
		}
	}
	if detailKinds > 1 {
		return nil, InvalidField("", "failureMatcher")
	}
	if !distinctKnown(match.Phases, failurePhases) || !distinctKnown(match.GRPCCodes, failureGRPCCodes) {
		return nil, InvalidField("", "failureMatcher")
	}
	statuses := make([]string, len(match.GRPCStatuses))
	for i, v := range match.GRPCStatuses {
		statuses[i] = string(v)
	}
	if !distinctKnown(statuses, failureGRPCStatuses) {
		return nil, InvalidField("grpcStatuses", "failureMatcher")
	}
	var statusSeen failureValues
	for _, status := range match.HTTPStatuses {
		if status < 100 || status > 599 || statusSeen.has(status-100) {
			return nil, InvalidField("httpStatuses", "failureMatcher")
		}
		statusSeen.add(status - 100)
	}
	codes := map[string]bool{}
	patterns := map[string]bool{}
	for _, pattern := range match.Codes {
		if len(pattern) > 64 || patterns[pattern] {
			return nil, InvalidField("codes", "failureCode")
		}
		patterns[pattern] = true
		prefix := strings.TrimSuffix(pattern, "*")
		wildcard := strings.HasSuffix(pattern, ".*") && len(pattern) > 2 && strings.Count(pattern, "*") == 1
		found := false
		for code, d := range definitions {
			if code != pattern && !(wildcard && strings.HasPrefix(code, prefix)) {
				continue
			}
			if !slices.Contains(d.Drivers, check.Type) {
				continue
			}
			if !d.Eligible && !none {
				return nil, InvalidField("codes", "failureCode")
			}
			codes[code], found = true, true
		}
		if !found {
			return nil, InvalidField("codes", "failureCode")
		}
	}
	keys := make([]string, 0, len(codes))
	for code := range codes {
		keys = append(keys, code)
	}
	slices.Sort(keys)
	clauses := make([]failureClause, 0, len(keys))
	for _, code := range keys {
		d := definitions[code]
		if check.Type == "grpc" {
			var config PulseGRPCConfig
			if json.Unmarshal(check.Config, &config) != nil {
				return nil, InvalidField("codes", "failureCode")
			}
			health := config.Mode != nil && string(*config.Mode) == "health"
			if (code == "grpc_health_failed" || code == "grpc.rpc_failed") && !health {
				return nil, InvalidField("codes", "failureCode")
			}
			// grpc-go exposes the health RPC status, not its underlying dial or
			// handshake error. Do not advertise rules this production adapter
			// cannot establish from typed evidence. TCP mode retains dial causes.
			if health && (strings.HasPrefix(code, "network.") || strings.HasPrefix(code, "dns.") || code == "dns_failed" || code == "connection_failed") {
				return nil, InvalidField("codes", "failureCode")
			}
		}
		c := failureClause{code: code}
		phases := match.Phases
		if len(phases) == 0 {
			phases = d.Phases
		}
		for _, phase := range phases {
			if !slices.Contains(d.Phases, phase) {
				return nil, InvalidField("phases", "failureMatcher")
			}
			c.phases |= 1 << slices.Index(failurePhases, phase)
		}
		if len(match.HTTPStatuses) > 0 && !slices.Contains(d.Details, "httpStatus") || len(match.GRPCCodes) > 0 && !slices.Contains(d.Details, "grpcCode") || len(statuses) > 0 && !slices.Contains(d.Details, "grpcStatus") {
			return nil, InvalidField("", "failureMatcher")
		}
		switch {
		case len(match.HTTPStatuses) > 0:
			c.values = statusSeen
		case len(match.GRPCCodes) > 0:
			for _, value := range match.GRPCCodes {
				c.values.add(slices.Index(failureGRPCCodes, value))
			}
		case len(statuses) > 0:
			for _, value := range statuses {
				c.values.add(16 + slices.Index(failureGRPCStatuses, value))
			}
		case slices.Contains(d.Details, "httpStatus"):
			for i := 0; i < 500; i++ {
				c.values.add(i)
			}
		case slices.Contains(d.Details, "grpcCode"):
			count := 16
			if slices.Contains(d.Details, "grpcStatus") {
				count = 19
			}
			for i := 0; i < count; i++ {
				c.values.add(i)
			}
		default:
			c.values.add(0)
		}
		clauses = append(clauses, c)
	}
	return clauses, nil
}

func distinctKnown(values, known []string) bool {
	for i, value := range values {
		if !slices.Contains(known, value) || slices.Contains(values[:i], value) {
			return false
		}
	}
	return true
}

func ruleCoverage(clauses []failureClause, previous []compiledRule) (overlap, covered bool) {
	covered = true
	for _, clause := range clauses {
		for phase := uint16(1); phase < 1<<len(failurePhases); phase <<= 1 {
			if clause.phases&phase == 0 {
				continue
			}
			remaining := clause.values
			for _, earlier := range previous {
				for _, c := range earlier.clauses {
					if c.code != clause.code || c.phases&phase == 0 {
						continue
					}
					for i, bits := range c.values {
						overlap = overlap || bits&clause.values[i] != 0
						remaining[i] &^= bits
					}
				}
			}
			if !remaining.empty() {
				covered = false
			}
		}
	}
	return
}

// ValidateFailureObservation checks typed, bounded fields without interpreting
// arbitrary messages. Missing observations and local unavailability cannot become
// eligible merely by supplying a different phase or protocol status.
func ValidateFailureObservation(driver string, observation FailureDiagnostic) error {
	_, _, ok := failureObservation(driver, observation)
	if !ok {
		return InvalidField("", "failureObservation")
	}
	return nil
}

func failureObservation(driver string, o FailureDiagnostic) (FailureDefinition, int, bool) {
	return failureObservationRegistry(driver, o, failureRegistry())
}
func failureObservationRegistry(driver string, o FailureDiagnostic, definitions map[string]FailureDefinition) (FailureDefinition, int, bool) {
	d, ok := definitions[o.Code]
	if !ok || o.TaxonomyVersion != FailureTaxonomyVersion || !slices.Contains(d.Drivers, driver) || !slices.Contains(d.Phases, o.Phase) || o.Message != "" && o.Message != d.Message {
		return d, 0, false
	}
	if (o.CertificateNotAfter != nil || o.CertificateDaysRemaining != nil) && !slices.Contains(d.Details, "certificateNotAfter") {
		return d, 0, false
	}
	if (o.CertificateNotAfter == nil) != (o.CertificateDaysRemaining == nil) {
		return d, 0, false
	}
	if o.CertificateNotAfter != nil && (o.CertificateNotAfter.IsZero() || o.CertificateNotAfter.Year() < 1 || o.CertificateNotAfter.Year() > 9999) || o.CertificateDaysRemaining != nil && (*o.CertificateDaysRemaining < -4000000 || *o.CertificateDaysRemaining > 4000000) {
		return d, 0, false
	}
	if o.HTTPStatus != nil && !slices.Contains(d.Details, "httpStatus") || o.GRPCCode != nil && !slices.Contains(d.Details, "grpcCode") || o.GRPCStatus != nil && !slices.Contains(d.Details, "grpcStatus") {
		return d, 0, false
	}
	if slices.Contains(d.Details, "httpStatus") {
		if o.HTTPStatus == nil || *o.HTTPStatus < 100 || *o.HTTPStatus > 599 {
			return d, 0, false
		}
		return d, *o.HTTPStatus - 100, true
	}
	if slices.Contains(d.Details, "grpcCode") {
		if (o.GRPCCode == nil) == (o.GRPCStatus == nil) {
			return d, 0, false
		}
		if o.GRPCCode != nil {
			index := slices.Index(failureGRPCCodes, *o.GRPCCode)
			if *o.GRPCCode == "Canceled" {
				d.Eligible, d.Available = false, false
			}
			return d, index, index >= 0
		}
		index := slices.Index(failureGRPCStatuses, string(*o.GRPCStatus))
		return d, 16 + index, index >= 0
	}
	return d, 0, true
}

// Match returns the first rule or the explicit default of no intervention.
// Invalid observations fail closed. An unavailable observation may explain a
// no-action rule but never permits recovery qualification.
func (p *CompiledRules) Match(observation FailureDiagnostic) RuleDecision {
	d, value, ok := failureObservationRegistry(p.driver, observation, p.definitions)
	if !ok || observation.TaxonomyVersion != p.version {
		return RuleDecision{Index: -1}
	}
	phase := uint16(1) << slices.Index(failurePhases, observation.Phase)
	for i, rule := range p.rules {
		for _, clause := range rule.clauses {
			if clause.code == observation.Code && clause.phases&phase != 0 && clause.values.has(value) {
				return RuleDecision{RuleID: rule.id, Index: i, Matched: true, Intervention: !rule.none && d.Eligible && d.Available, Eligible: d.Eligible && d.Available}
			}
		}
	}
	return RuleDecision{Index: -1, Eligible: d.Eligible && d.Available}
}
