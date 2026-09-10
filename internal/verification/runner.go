// Package verification runs explicitly configured live driver scenarios.
package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"cpra/internal/version"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
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
	Kind       string        `yaml:"kind"`
	Driver     string        `yaml:"driver"`
	Configured bool          `yaml:"configured"`
	MonitorID  string        `yaml:"monitor_id"`
	Color      string        `yaml:"color"`
	Endpoint   int           `yaml:"endpoint"`
	Observer   []string      `yaml:"observer"`
	Timeout    time.Duration `yaml:"timeout"`
}
type Config struct {
	Manifest schema.Manifest `yaml:"manifest"`
	Cases    []Case          `yaml:"cases"`
}
type Record struct {
	Driver
	Status         string    `json:"status"`
	Operation      string    `json:"operation"`
	Accepted       bool      `json:"accepted"`
	Observed       bool      `json:"observed"`
	Started        time.Time `json:"started,omitempty"`
	DurationMS     float64   `json:"duration_ms"`
	EvidenceRef    string    `json:"evidence_ref,omitempty"`
	EvidenceSHA256 string    `json:"evidence_sha256,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}
type Report struct {
	RunID     string    `json:"run_id"`
	Version   string    `json:"version"`
	GoVersion string    `json:"go_version"`
	Generated time.Time `json:"generated"`
	Complete  bool      `json:"all_providers_verified"`
	Records   []Record  `json:"records"`
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
	report := Report{RunID: uuid.NewString(), Version: version.Info(), GoVersion: runtime.Version(), Generated: time.Now().UTC(), Complete: true}
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
		cases[key] = c
	}
	monitors := map[string]schema.Monitor{}
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
		record := Record{Driver: driver, Status: "not_configured", Operation: driver.Kind, Reason: "not verified: no enabled user configuration"}
		c, exists := cases[driver.Kind+"/"+driver.Name]
		if exists && c.Configured {
			if !live {
				return report, fmt.Errorf("configured live operations require explicit -live invocation")
			}
			m, ok := monitors[c.MonitorID]
			raw, _ := json.Marshal(m)
			if !ok || len(c.Observer) == 0 || bytes.Contains(raw, []byte("REPLACE_WITH")) || strings.Contains(strings.Join(c.Observer, " "), "REPLACE_WITH") {
				record.Reason = "not verified: monitor or independent observer is not configured"
			} else {
				record = runCase(ctx, report.RunID, driver, c, m, cfg.Manifest)
			}
		}
		if record.Status != "pass" {
			report.Complete = false
		}
		report.Records = append(report.Records, record)
	}
	return report, nil
}

func runCase(parent context.Context, runID string, driver Driver, c Case, m schema.Monitor, manifest schema.Manifest) (r Record) {
	r = Record{Driver: driver, Status: "fail", Operation: driver.Kind, Started: time.Now().UTC()}
	defer func() { r.DurationMS = float64(time.Since(r.Started)) / float64(time.Millisecond) }()
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	m.Name = m.Name + " [CPRa verification " + runID + "]"
	job, err := configuredJob(driver, c, m, manifest)
	if err != nil {
		r.Reason = "driver or selected monitor configuration is unavailable in this build"
		return r
	}
	before, err := observe(ctx, c.Observer, runID, "before", nil)
	if err != nil {
		r.Reason = "independent baseline observation failed; operation was not invoked"
		return r
	}
	d := jobs.NewDispatch(job, ecs.Entity{}, driver.Kind, c.Color, 1, c.Endpoint)
	d.SetContext(ctx)
	result := d.Execute()
	r.Accepted = result.Err == nil
	r.DurationMS = float64(time.Since(r.Started)) / float64(time.Millisecond)
	if result.Err != nil {
		r.Reason = "production driver returned an error; inspect designated provider records"
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
	evidence, err := json.Marshal(map[string]any{"run_id": runID, "kind": driver.Kind, "driver": driver.Name, "before": redactedObservation(before), "after": redactedObservation(after)})
	if err != nil {
		r.Reason = "invalid observer evidence"
		return r
	}
	digest := sha256.Sum256(evidence)
	if dir := os.Getenv("CPRA_VERIFY_EVIDENCE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			r.Reason = "cannot retain independent evidence"
			return r
		}
		name := runID + "-" + driver.Kind + "-" + driver.Name + ".jsonl"
		// Observers are explicitly configured to emit redacted evidence. These
		// private artifacts are never returned by the public CPRa API.
		if err := os.WriteFile(filepath.Join(dir, name), evidence, 0600); err != nil {
			r.Reason = "cannot retain independent evidence"
			return r
		}
		r.EvidenceRef = name
	}
	r.EvidenceSHA256 = hex.EncodeToString(digest[:])
	r.Observed = true
	r.Status = "pass"
	r.DurationMS = float64(time.Since(r.Started)) / float64(time.Millisecond)
	return r
}

func configuredJob(driver Driver, c Case, m schema.Monitor, manifest schema.Manifest) (jobs.Job, error) {
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
			names := manifest.NotificationGroups[code.NotifyGroup]
			if c.Endpoint < 0 || c.Endpoint >= len(names) {
				return nil, fmt.Errorf("invalid endpoint index")
			}
			typeName = manifest.Endpoints[names[c.Endpoint]].Type
		}
		if typeName != driver.Name {
			return nil, fmt.Errorf("notification type mismatch")
		}
		list, err := jobs.CreateCodeJobs(m.Name, code, ecs.Entity{}, c.Color, manifest.Endpoints, manifest.NotificationGroups)
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
	for _, key := range []string{"resource_digest", "generation_digest", "previous_generation_digest"} {
		if value, ok := row[key].(string); ok && len(value) == 64 {
			if _, err := hex.DecodeString(value); err == nil {
				safe[key] = value
			}
		}
	}
	return safe
}
