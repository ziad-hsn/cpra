package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const RecoveryRulesFormatVersion = 48
const recoveryRulesSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-48\n"

func recoveryRulesStorageFormat(version int) bool {
	return version == RecoveryRulesFormatVersion || dispatchStorageFormat(version) || version == 49 && externalStorageFormat(version)
}

var ErrRecoveryRules = errors.New("invalid recovery rule state")

// RecoveryRuleTarget contains only the selected driver's identity, its execution
// budget and a digest of its configuration/dependencies. It cannot execute work.
type RecoveryRuleTarget struct {
	RuleID  string        `json:"rule_id"`
	Driver  string        `json:"driver"`
	Binding string        `json:"binding"`
	Budget  time.Duration `json:"budget"`
}

type recoveryRuleInputs struct {
	recoveryRuleExtensions
	Driver   string               `json:"driver"`
	Mode     string               `json:"mode,omitempty"`
	Taxonomy string               `json:"taxonomy"`
	Rules    []api.RulePredicate  `json:"rules"`
	Targets  []RecoveryRuleTarget `json:"targets"`
}

type recoveryRuleData struct {
	inputs   recoveryRuleInputs
	encoded  []byte
	digest   string
	compiled *api.CompiledRules
	// Membership is private cache bookkeeping; the policy and program stay
	// immutable. A rebuilt cache cannot mistake a snapshot's token for ownership.
	intern *recoveryProgramEntry
}

// RecoveryRules is an immutable value. Copies share only immutable inputs and
// compiled predicates; decoding replaces the receiver, never shared data.
type RecoveryRules struct{ data *recoveryRuleData }

func (p RecoveryRules) IsZero() bool          { return p.data == nil }
func (p RecoveryRules) HasIntervention() bool { return p.data != nil && len(p.data.inputs.Targets) > 0 }
func (p RecoveryRules) Digest() string {
	if p.data == nil {
		return ""
	}
	return p.data.digest
}
func (p RecoveryRules) Taxonomy() string {
	if p.data == nil {
		return ""
	}
	return p.data.inputs.Taxonomy
}
func (p RecoveryRules) Driver() string {
	if p.data == nil {
		return ""
	}
	return p.data.inputs.Driver
}
func (p RecoveryRules) Target(id string) (RecoveryRuleTarget, bool) {
	if p.data != nil {
		for _, target := range p.data.inputs.Targets {
			if target.RuleID == id {
				return target, true
			}
		}
	}
	return RecoveryRuleTarget{}, false
}
func (p RecoveryRules) Match(failure FailureDiagnostic) api.RuleDecision {
	if p.data == nil {
		return api.RuleDecision{}
	}
	return p.data.compiled.Match(failure.Public())
}
func (p RecoveryRules) MarshalJSON() ([]byte, error) {
	if p.data == nil {
		return []byte("null"), nil
	}
	return append([]byte(nil), p.data.encoded...), nil
}
func (p *RecoveryRules) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return ErrRecoveryRules
	}
	var input recoveryRuleInputs
	if err := api.StrictDecode(data, &input); err != nil {
		return ErrRecoveryRules
	}
	v, err := newRecoveryRules(input)
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// NewRecoveryRules freezes credential-free inputs. Every branch must already
// have passed the shared complete-resource validation before this constructor.
func NewRecoveryRules(driver, mode, taxonomy string, rules []api.RulePredicate, targets []RecoveryRuleTarget) (RecoveryRules, error) {
	// Detach caller-owned slices before freezing them. This runs only at policy
	// preparation/restoration, never for a health observation.
	data, err := json.Marshal(recoveryRuleInputs{Driver: driver, Mode: mode, Taxonomy: taxonomy, Rules: rules, Targets: targets})
	if err != nil {
		return RecoveryRules{}, ErrRecoveryRules
	}
	var result RecoveryRules
	err = json.Unmarshal(data, &result)
	return result, err
}

func newRecoveryRules(input recoveryRuleInputs) (RecoveryRules, error) {
	compiled, _, err := compileRecoveryRuleInputs(input)
	if err != nil {
		return RecoveryRules{}, ErrRecoveryRules
	}
	seen := map[string]bool{}
	for _, target := range input.Targets {
		if seen[target.RuleID] || !catalogIdentifier(target.Driver, 128) || !bootstrapHash(target.Binding) || target.Budget <= 0 || target.Budget > 24*time.Hour {
			return RecoveryRules{}, ErrRecoveryRules
		}
		seen[target.RuleID] = true
	}
	for _, rule := range input.Rules {
		if seen[rule.ID] == rule.None {
			return RecoveryRules{}, ErrRecoveryRules
		}
		delete(seen, rule.ID)
	}
	if len(seen) != 0 {
		return RecoveryRules{}, ErrRecoveryRules
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 1024*1024 {
		return RecoveryRules{}, ErrRecoveryRules
	}
	digest := sha256.Sum256(encoded)
	return RecoveryRules{&recoveryRuleData{inputs: input, encoded: encoded, digest: hex.EncodeToString(digest[:]), compiled: compiled}}, nil
}

// RecoveryDecision is compact current qualification, not retained observation
// history. A selection is pinned only by a committed action start.
type RecoveryDecision struct {
	Revision    string            `json:"revision"`
	RuleID      string            `json:"rule_id,omitempty"`
	Count       int               `json:"count"`
	Observation uint64            `json:"observation"`
	ObservedAt  time.Time         `json:"observed_at,omitzero"`
	Reason      string            `json:"reason"`
	Suppressed  bool              `json:"suppressed,omitempty"`
	Selection   RecoverySelection `json:"selection,omitzero"`
	Deadline    time.Time         `json:"deadline,omitzero"`
}

type RecoverySelection struct {
	RuleID        string             `json:"rule_id"`
	PolicyDigest  string             `json:"policy_digest"`
	Configuration string             `json:"configuration"`
	Decision      string             `json:"decision"`
	Observation   uint64             `json:"observation"`
	IncidentID    string             `json:"incident_id"`
	Target        RecoveryRuleTarget `json:"target"`
}

func (m *Monitor) resetRuleQualification(reason string) {
	if m.Policy.RecoveryRules.IsZero() && m.RecoveryDecision == (RecoveryDecision{}) {
		return
	}
	d := &m.RecoveryDecision
	d.RuleID, d.Count, d.Reason = "", 0, reason
	d.Revision = identity(fmt.Sprintf("rules/%s/%s/%s/%d/%s", m.ID, m.Revision, m.ControlRevision, m.Generation, reason))
}

func (m *Monitor) observeRecoveryRule(c Command) {
	if m.Policy.RecoveryRules.IsZero() {
		return
	}
	d := &m.RecoveryDecision
	if c.Generation <= d.Observation {
		return
	}
	if !d.ObservedAt.IsZero() && m.Policy.MaintenanceBetween(d.ObservedAt, c.At) {
		m.resetRuleQualification("fresh_observations_required")
	}
	d.Observation = c.Generation
	d.ObservedAt = c.At
	suppressed := c.Maintenance || m.Ignored || !m.Policy.Enabled || m.snoozed(c.At) || m.pendingAutomaticIgnore(c.At) || m.awaitingOutsideConfirmation()
	if suppressed {
		m.resetRuleQualification("suppressed")
		d.Suppressed = true
		return
	}
	if d.Suppressed {
		m.resetRuleQualification("fresh_observations_required")
		d.Suppressed = false
	}
	if c.Outcome != "failure" && c.Outcome != "timeout" {
		m.resetRuleQualification("observation_not_eligible")
		return
	}
	match := m.Policy.RecoveryRules.Match(c.Failure)
	if !match.Matched {
		m.resetRuleQualification("no_matching_rule")
		return
	}
	if !match.Intervention {
		m.resetRuleQualification("no_intervention")
		d.RuleID = match.RuleID
		return
	}
	if d.RuleID != match.RuleID {
		d.RuleID, d.Count = match.RuleID, 0
	}
	d.Count = min(max(1, m.Policy.Unhealthy), d.Count+1)
	d.Reason = "qualifying"
	if d.Count >= max(1, m.Policy.Unhealthy) {
		d.Reason = "qualified"
	}
	if d.Selection != (RecoverySelection{}) && (d.Selection.RuleID != d.RuleID || d.Selection.Configuration != m.Revision || d.Selection.PolicyDigest != m.Policy.RecoveryRules.Digest()) {
		d.Reason = "incident_selection_pinned"
	}
	d.Revision = identity(fmt.Sprintf("rules/%s/%s/%s/%d/%s/%d", m.ID, m.Revision, m.ControlRevision, c.Generation, d.RuleID, d.Count))
}

func (m Monitor) recoveryRuleQualified() bool {
	if m.Policy.RecoveryRules.IsZero() {
		return m.RecoveryDecision.Selection == (RecoverySelection{})
	}
	d := m.RecoveryDecision
	if m.recoveryAttempts() > 0 && d.Selection == (RecoverySelection{}) {
		if previous, ok := m.latestRecovery(); ok && previous.Rule == (RecoverySelection{}) {
			return false
		}
	}
	return d.Reason == "qualified" && d.Count >= max(1, m.Policy.Unhealthy) && d.Observation == m.Generation
}

func (m Monitor) recoveryRuleBinding() RecoverySelection {
	if m.Policy.RecoveryRules.IsZero() || !m.recoveryRuleQualified() {
		return RecoverySelection{}
	}
	if m.RecoveryDecision.Selection != (RecoverySelection{}) {
		return m.RecoveryDecision.Selection
	}
	target, ok := m.Policy.RecoveryRules.Target(m.RecoveryDecision.RuleID)
	if !ok {
		return RecoverySelection{}
	}
	return RecoverySelection{RuleID: target.RuleID, PolicyDigest: m.Policy.RecoveryRules.Digest(), Configuration: m.Revision, Decision: m.RecoveryDecision.Revision, Observation: m.RecoveryDecision.Observation, IncidentID: m.IncidentID, Target: target}
}

func (m Monitor) recoveryRuleStartAllowed(a Action) bool {
	if a.Rule == (RecoverySelection{}) {
		return m.Policy.RecoveryRules.IsZero() && m.RecoveryDecision.Selection == (RecoverySelection{})
	}
	if !m.recoveryRuleQualified() || a.Rule.IncidentID != m.IncidentID || a.Rule.Configuration != m.Revision || a.Rule.PolicyDigest != m.Policy.RecoveryRules.Digest() || a.Rule.RuleID != m.RecoveryDecision.RuleID {
		return false
	}
	target, ok := m.Policy.RecoveryRules.Target(a.Rule.RuleID)
	return ok && target == a.Rule.Target && (m.RecoveryDecision.Selection == (RecoverySelection{}) || m.RecoveryDecision.Selection == a.Rule)
}

func (m *Monitor) cancelObsoleteRuleIntent(at time.Time, emit func(string, *Action)) {
	if m.Policy.RecoveryRules.IsZero() {
		return
	}
	for _, id := range sortedActions(m.Actions) {
		a := m.Actions[id]
		if a.Kind != "intervention" || a.State != Queued || !m.OwnsAction(a) || m.recoveryRuleStartAllowed(a) {
			continue
		}
		a.State, a.Outcome, a.FinishedAt = Cancelled, "recovery_rule_changed", at
		m.Actions[id] = a
		emit("action_cancelled", &a)
	}
}

// Only a definitely unstarted intent invalidated by policy or suppression may
// be replaced. The incident's pinned selection, attempt counter and absolute
// deadline remain authoritative; this never replays an uncertain effect.
func (a Action) replaceableRuleIntent() bool {
	if a.Rule == (RecoverySelection{}) || a.State != Cancelled || !a.StartedAt.IsZero() || a.FinishedAt.IsZero() {
		return false
	}
	switch a.Outcome {
	case "recovery_rule_changed", "monitor_ignored", "snooze", "maintenance", "configuration_changed", "monitor_disabled":
		return true
	}
	return false
}

func requiresRecoveryRules(m Monitor) bool {
	if !m.Policy.RecoveryRules.IsZero() || m.RecoveryDecision != (RecoveryDecision{}) || m.LastFailure.TaxonomyVersion != "" || m.ConfigurationProblem.TaxonomyVersion != "" {
		return true
	}
	if m.ActionProjection != nil && m.ActionProjection.LatestRecovery != nil && m.ActionProjection.LatestRecovery.Rule != (RecoverySelection{}) {
		return true
	}
	for _, a := range m.Actions {
		if a.Rule != (RecoverySelection{}) || a.Cause.TaxonomyVersion != "" || a.Failure.TaxonomyVersion != "" {
			return true
		}
	}
	return false
}

func validateRecoveryRuleState(m Monitor, version int) error {
	if !requiresRecoveryRules(m) {
		return nil
	}
	if !recoveryRulesStorageFormat(version) {
		return ErrRecoveryRules
	}
	if !m.Policy.RecoveryRules.IsZero() && !bootstrapHash(m.Revision) {
		return ErrRecoveryRules
	}
	d := m.RecoveryDecision
	if (d.Observation == 0) != d.ObservedAt.IsZero() || !d.ObservedAt.IsZero() && (d.ObservedAt.Year() < 1 || d.ObservedAt.Year() > 9998) || !d.Deadline.IsZero() && (d.Deadline.Year() < 1 || d.Deadline.Year() > 9998) || d.Count > 0 && (d.RuleID == "" || d.Revision == "" || d.Observation == 0) {
		return ErrRecoveryRules
	}
	if d.Count < 0 || d.Count > max(1, m.Policy.Unhealthy) || d.Observation > m.Generation || len(d.Reason) > 128 || d.Revision != "" && !bootstrapHash(d.Revision) {
		return ErrRecoveryRules
	}

	if err := validateRecoverySelection(d.Selection, version); err != nil {
		return err
	}
	for _, a := range m.Actions {
		if a.Rule != (RecoverySelection{}) && (a.Kind != "intervention" || a.Rule.Configuration != a.Revision || a.Rule.IncidentID != a.IncidentID) {
			return ErrRecoveryRules
		}
		if err := validateRecoverySelection(a.Rule, version); err != nil {
			return err
		}
	}
	return nil
}

func validateRecoverySelection(s RecoverySelection, version int) error {
	if s == (RecoverySelection{}) {
		return nil
	}
	if !recoveryRulesStorageFormat(version) {
		return ErrRecoveryRules
	}
	if s.RuleID != s.Target.RuleID || !catalogIdentifier(s.RuleID, 64) || !bootstrapHash(s.PolicyDigest) || !bootstrapHash(s.Configuration) || !bootstrapHash(s.Decision) || s.Observation == 0 || !catalogIdentifier(s.IncidentID, 256) || !bootstrapHash(s.Target.Binding) || !catalogIdentifier(s.Target.Driver, 128) || s.Target.Budget <= 0 || s.Target.Budget > 24*time.Hour {
		return ErrRecoveryRules
	}
	return nil
}

func validateActionRule(a Action, version int) error {
	if err := validateRecoverySelection(a.Rule, version); err != nil {
		return err
	}
	if a.Rule != (RecoverySelection{}) && (a.Kind != "intervention" || a.Rule.Configuration != a.Revision || a.Rule.IncidentID != a.IncidentID) {
		return ErrRecoveryRules
	}
	return nil
}
