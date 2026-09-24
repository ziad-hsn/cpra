// Package drivertest runs explicitly configured driver scenarios and records
// the boundary of the evidence, from mock contracts to live account effects.
package drivertest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/version"
	"gopkg.in/yaml.v3"
)

type Driver struct {
	Kind string `json:"kind"`
	Name string `json:"driver"`
}

func Inventory() []Driver {
	var out []Driver
	for _, group := range []struct{ kind, names string }{{"pulse", "http tcp icmp dns udp grpc docker tls redis postgres mysql mongo rabbitmq kafka"}, {"intervention", "docker webhook kubernetes aws systemd"}, {"code", "log slack pagerduty email webhook telegram discord opsgenie mattermost victorops pushover datadog teams twilio"}} {
		for _, name := range strings.Fields(group.names) {
			out = append(out, Driver{group.kind, name})
		}
	}
	return out
}

type Case struct {
	Kind                string        `yaml:"kind"`
	Driver              string        `yaml:"driver"`
	Configured          bool          `yaml:"configured"`
	MonitorID           string        `yaml:"monitor_id"`
	Color               string        `yaml:"color"`
	Endpoint            int           `yaml:"endpoint"`
	Observer            []string      `yaml:"observer"`
	Timeout             time.Duration `yaml:"timeout"`
	EvidenceType        string        `yaml:"evidence_type"`
	ObservationBoundary string        `yaml:"observation_boundary"`
}

const (
	EvidenceLocalIntegration = "local_integration"
	EvidenceMockContract     = "mock_contract"
	EvidenceProviderSandbox  = "provider_sandbox"
	EvidenceLiveAccount      = "live_account"
	BoundaryEffect           = "effect"
	BoundaryAPIAcceptance    = "api_acceptance"
)

type Config struct {
	Manifest manifest.Manifest `yaml:"manifest"`
	Cases    []Case            `yaml:"cases"`
}
type Record struct {
	Driver
	Status              string    `json:"status"`
	Operation           string    `json:"operation"`
	Accepted            bool      `json:"accepted"`
	Observed            bool      `json:"observed"`
	Invoked             bool      `json:"operation_invoked"`
	HTTPStatus          int       `json:"http_status,omitempty"` // Typed HTTP rejection only; zero for transport/setup failures.
	Started             time.Time `json:"started,omitempty"`
	DurationMS          float64   `json:"duration_ms"`
	EvidenceRef         string    `json:"evidence_ref,omitempty"`
	EvidenceSHA256      string    `json:"evidence_sha256,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	Configured          bool      `json:"configured"`
	EvidenceType        string    `json:"evidence_type"`
	ObservationBoundary string    `json:"observation_boundary"`
}
type Report struct {
	RunID                string         `json:"run_id"`
	Version              string         `json:"version"`
	GoVersion            string         `json:"go_version"`
	Generated            time.Time      `json:"generated"`
	Complete             bool           `json:"all_providers_verified"`
	Records              []Record       `json:"records"`
	AllConfiguredPassed  bool           `json:"all_configured_passed"`
	AllDriversTested     bool           `json:"all_drivers_tested"`
	AllDriversPassed     bool           `json:"all_drivers_passed"`
	ConfiguredCases      int            `json:"configured_cases"`
	ExecutedCases        int            `json:"executed_cases"`
	PassedCases          int            `json:"passed_cases"`
	FailedCases          int            `json:"failed_cases"`
	NotConfiguredCases   int            `json:"not_configured_cases"`
	PassedByEvidenceType map[string]int `json:"passed_by_evidence_type"`
}

func Load(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	d := yaml.NewDecoder(io.LimitReader(f, 4<<20))
	d.KnownFields(true)
	if err = d.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("invalid live verification configuration (check field names and value types)")
	}
	var trailing any
	if err = d.Decode(&trailing); err != io.EOF {
		return cfg, fmt.Errorf("live configuration must contain one document")
	}
	return cfg, nil
}

// Run does not replace the driver's transport. An observer is a separately
// configured executable, called before and after, with no shell expansion.
// It must correlate the designated resource or destination and the run ID.
func Run(ctx context.Context, cfg Config, live bool) (Report, error) {
	report := Report{RunID: uuid.NewString(), Version: version.Info(), GoVersion: runtime.Version(), Generated: time.Now().UTC()}
	cases := map[string]Case{}
	known := map[string]bool{}
	for _, driver := range Inventory() {
		known[driver.Kind+"/"+driver.Name] = true
	}
	for _, c := range cfg.Cases {
		key := c.Kind + "/" + c.Driver
		if !known[key] {
			return report, fmt.Errorf("unknown verification driver %q", key)
		}
		if _, exists := cases[key]; exists {
			return report, fmt.Errorf("duplicate verification case %q", key)
		}
		var err error
		c, err = normalizeCase(c)
		if err != nil {
			return report, err
		}
		if c.Configured && !live {
			return report, fmt.Errorf("configured verification operations require explicit -live invocation")
		}
		cases[key] = c
	}
	monitors := map[string]manifest.Monitor{}
	for _, m := range cfg.Manifest.Monitors {
		id, err := m.EffectiveID()
		if err != nil {
			return report, err
		}
		if _, exists := monitors[id]; exists {
			return report, fmt.Errorf("duplicate monitor ID")
		}
		monitors[id] = m
	}
	for _, driver := range Inventory() {
		record := Record{Driver: driver, Status: "not_configured", Operation: driver.Kind, EvidenceType: EvidenceLiveAccount, ObservationBoundary: BoundaryEffect, Reason: "not verified: no enabled user configuration"}
		c, exists := cases[driver.Kind+"/"+driver.Name]
		if exists {
			record.Configured = c.Configured
			record.EvidenceType = c.EvidenceType
			record.ObservationBoundary = c.ObservationBoundary
		}
		if exists && c.Configured {
			m, ok := monitors[c.MonitorID]
			if !ok || !caseConfigured(c, m, cfg.Manifest) {
				record.Reason = "not verified: selected target, test credentials, or independent observer is not configured"
			} else {
				record = runCase(ctx, report.RunID, driver, c, m, cfg.Manifest)
			}
		}
		report.Records = append(report.Records, record)
	}
	report.summarize()
	return report, nil
}

// Validate the entire evidence declaration before any observer or driver runs.
// Existing configurations omit the classification and retain live-account semantics.
func normalizeCase(c Case) (Case, error) {
	if c.EvidenceType == "" {
		c.EvidenceType = EvidenceLiveAccount
	}
	switch c.EvidenceType {
	case EvidenceLocalIntegration, EvidenceMockContract, EvidenceProviderSandbox, EvidenceLiveAccount:
	default:
		return c, fmt.Errorf("invalid verification evidence_type")
	}
	if c.ObservationBoundary == "" {
		c.ObservationBoundary = BoundaryEffect
	}
	switch c.ObservationBoundary {
	case BoundaryEffect:
	case BoundaryAPIAcceptance:
		if c.EvidenceType != EvidenceProviderSandbox || c.Kind != "code" || c.Driver != "twilio" {
			return c, fmt.Errorf("api_acceptance requires the Twilio provider_sandbox test-credentials scenario")
		}
	default:
		return c, fmt.Errorf("invalid verification observation_boundary")
	}
	return c, nil
}

func caseConfigured(c Case, m manifest.Monitor, configuration manifest.Manifest) bool {
	if c.ObservationBoundary != BoundaryAPIAcceptance && (len(c.Observer) == 0 || strings.TrimSpace(c.Observer[0]) == "") {
		return false
	}
	if strings.Contains(strings.Join(c.Observer, " "), "REPLACE_WITH") {
		return false
	}
	// Include referenced endpoint configuration in placeholder checks. Otherwise
	// an enabled notification-group case can send literal example credentials.
	var selected any
	switch c.Kind {
	case "pulse":
		selected = m.Pulse
	case "intervention":
		selected = m.Intervention
	case "code":
		code, ok := m.Codes[c.Color]
		if !ok {
			return false
		}
		selected = code.Config
		if code.NotifyGroup != "" {
			names := configuration.NotificationGroups[code.NotifyGroup]
			if c.Endpoint < 0 || c.Endpoint >= len(names) {
				return false
			}
			endpoint, ok := configuration.Endpoints[names[c.Endpoint]]
			if !ok {
				return false
			}
			selected = endpoint.Config
		}
	}
	if c.ObservationBoundary == BoundaryAPIAcceptance {
		// The documented magic sender is deliberately unusable with live
		// credentials. Require the success test vector before waiving an
		// independent receipt observer for this no-send scenario.
		twilio, ok := selected.(*manifest.CodeNotificationTwilio)
		if !ok || twilio == nil || twilio.From != "+15005550006" || strings.TrimSpace(twilio.AccountSID) == "" || strings.TrimSpace(twilio.AuthToken) == "" || strings.TrimSpace(twilio.To) == "" {
			return false
		}
	}
	raw, err := json.Marshal(selected)
	return err == nil && !bytes.Contains(raw, []byte("REPLACE_WITH"))
}

func (r *Report) summarize() {
	r.ConfiguredCases, r.ExecutedCases, r.PassedCases, r.FailedCases, r.NotConfiguredCases = 0, 0, 0, 0, 0
	r.PassedByEvidenceType = map[string]int{}
	liveEffects := 0
	for _, row := range r.Records {
		if row.Configured {
			r.ConfiguredCases++
		}
		if row.Invoked {
			r.ExecutedCases++
		}
		switch row.Status {
		case "pass":
			r.PassedCases++
			r.PassedByEvidenceType[row.EvidenceType]++
			if row.EvidenceType == EvidenceLiveAccount && row.ObservationBoundary == BoundaryEffect && row.Accepted && row.Observed {
				liveEffects++
			}
		case "fail":
			r.FailedCases++
		case "not_configured":
			r.NotConfiguredCases++
		}
	}
	r.AllConfiguredPassed = r.ConfiguredCases > 0 && r.PassedCases == r.ConfiguredCases
	r.AllDriversTested = len(r.Records) == len(Inventory()) && r.ExecutedCases == len(Inventory())
	r.AllDriversPassed = len(r.Records) == len(Inventory()) && r.PassedCases == len(Inventory())
	r.Complete = len(r.Records) == len(Inventory()) && liveEffects == len(Inventory())
}

func runCase(parent context.Context, runID string, driver Driver, c Case, m manifest.Monitor, configuration manifest.Manifest) (r Record) {
	r = Record{Driver: driver, Status: "fail", Operation: driver.Kind, Configured: true, EvidenceType: c.EvidenceType, ObservationBoundary: c.ObservationBoundary, Started: time.Now().UTC()}
	defer func() { r.DurationMS = float64(time.Since(r.Started)) / float64(time.Millisecond) }()
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	m.Name = m.Name + " [CPRa verification " + runID + "]"
	job, err := configuredJob(driver, c, m, configuration)
	if err != nil {
		r.Reason = "driver or selected monitor configuration is unavailable in this build"
		return r
	}
	var before []byte
	if c.ObservationBoundary != BoundaryAPIAcceptance {
		before, err = observe(ctx, c.Observer, runID, "before", nil)
		if err != nil {
			r.Reason = "independent baseline observation failed; operation was not invoked"
			return r
		}
	}
	d := jobs.NewDispatch(job, ecs.Entity{}, driver.Kind, c.Color, 1, c.Endpoint)
	d.SetContext(ctx)
	r.Invoked = true
	result := d.Execute()
	r.Accepted = result.Err == nil
	r.DurationMS = float64(time.Since(r.Started)) / float64(time.Millisecond)
	if result.Err != nil {
		var rejection *jobs.DeliveryError
		if errors.As(result.Err, &rejection) {
			r.HTTPStatus = rejection.Status
		}
		r.Reason = "production driver returned an error; inspect designated provider records"
		return r
	}
	if c.ObservationBoundary == BoundaryAPIAcceptance {
		// Twilio test credentials deliberately never send an SMS or emit a
		// delivery callback. Passing here attests only to driver/API acceptance.
		r.Status = "pass"
		r.Reason = "test API accepted operation; delivery is not performed or observed"
		retainEvidence(&r, runID, nil, nil)
		return r
	}
	after, err := observe(ctx, c.Observer, runID, "after", before)
	if err != nil {
		r.Reason = "provider accepted operation; independent effect observation failed"
		return r
	}
	var observation struct {
		Observed bool `json:"observed"`
	}
	if err = json.Unmarshal(after, &observation); err != nil || !observation.Observed {
		r.Reason = "provider acceptance does not establish observed completion or receipt"
		return r
	}
	r.Observed = true
	r.Status = "pass"
	retainEvidence(&r, runID, before, after)
	return r
}

func retainEvidence(r *Record, runID string, before, after []byte) {
	row := map[string]any{"run_id": runID, "kind": r.Kind, "driver": r.Name, "evidence_type": r.EvidenceType, "observation_boundary": r.ObservationBoundary, "accepted": r.Accepted, "observed": r.Observed}
	if before != nil {
		row["before"] = redactedObservation(before)
	}
	if after != nil {
		row["after"] = redactedObservation(after)
	}
	evidence, err := json.Marshal(row)
	if err != nil {
		r.Status = "fail"
		r.Reason = "invalid observer evidence"
		return
	}
	digest := sha256.Sum256(evidence)
	if dir := os.Getenv("CPRA_VERIFY_EVIDENCE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			r.Status = "fail"
			r.Reason = "cannot retain independent evidence"
			return
		}
		name := runID + "-" + r.Kind + "-" + r.Name + ".jsonl"
		// Observers are explicitly configured to emit redacted evidence. These
		// private artifacts are never returned by the public CPRa API.
		if err := os.WriteFile(filepath.Join(dir, name), evidence, 0600); err != nil {
			r.Status = "fail"
			r.Reason = "cannot retain independent evidence"
			return
		}
		r.EvidenceRef = name
	}
	r.EvidenceSHA256 = hex.EncodeToString(digest[:])
}

func configuredJob(driver Driver, c Case, m manifest.Monitor, configuration manifest.Manifest) (jobs.Job, error) {
	switch driver.Kind {
	case "pulse":
		if m.Pulse.Type != driver.Name {
			return nil, fmt.Errorf("check type mismatch")
		}
		return jobs.CreatePulseJob(m.Pulse, ecs.Entity{})
	case "intervention":
		if m.Intervention.Target == nil || m.Intervention.Target.GetTargetType() != driver.Name {
			return nil, fmt.Errorf("recovery type mismatch")
		}
		return jobs.CreateInterventionJob(m.Intervention, ecs.Entity{})
	case "code":
		code, ok := m.Codes[c.Color]
		if !ok {
			return nil, fmt.Errorf("missing alert color")
		}
		typeName := code.Notify
		if code.NotifyGroup != "" {
			names := configuration.NotificationGroups[code.NotifyGroup]
			if c.Endpoint < 0 || c.Endpoint >= len(names) {
				return nil, fmt.Errorf("invalid endpoint index")
			}
			typeName = configuration.Endpoints[names[c.Endpoint]].Type
		}
		if typeName != driver.Name {
			return nil, fmt.Errorf("notification type mismatch")
		}
		list, err := jobs.CreateCodeJobs(m.Name, code, ecs.Entity{}, c.Color, configuration.Endpoints, configuration.NotificationGroups)
		if err != nil {
			return nil, err
		}
		if c.Endpoint < 0 || c.Endpoint >= len(list) {
			return nil, fmt.Errorf("missing endpoint")
		}
		return list[c.Endpoint], nil
	}
	return nil, fmt.Errorf("unknown pipeline")
}

type boundedOutput struct {
	data  []byte
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.limit {
		return 0, fmt.Errorf("observer output exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func observe(ctx context.Context, args []string, runID, phase string, before []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "CPRA_VERIFY_RUN_ID="+runID, "CPRA_VERIFY_PHASE="+phase)
	cmd.Stdin = bytes.NewReader(before)
	stdout := &boundedOutput{limit: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.data, nil
}

// Persist a numeric/boolean allowlist and fingerprints rather than arbitrary
// user/provider text. Unknown fields and URL/token material never enter reports.
func redactedObservation(data []byte) map[string]any {
	digest := sha256.Sum256(data)
	safe := map[string]any{"source_sha256": hex.EncodeToString(digest[:])}
	var row map[string]any
	if json.Unmarshal(data, &row) != nil {
		return safe
	}
	for _, key := range []string{"observed", "count", "before", "after", "ready", "running"} {
		switch v := row[key].(type) {
		case bool:
			safe[key] = v
		case float64:
			safe[key] = v
		}
	}
	for _, key := range []string{"request_count", "reply_count"} {
		if value, ok := row[key].(float64); ok {
			safe[key] = value
		}
	}
	for _, key := range []string{"resource_digest", "generation_digest", "previous_generation_digest"} {
		if value, ok := row[key].(string); ok && len(value) == 64 {
			if _, err := hex.DecodeString(value); err == nil {
				safe[key] = value
			}
		}
	}
	return safe
}
