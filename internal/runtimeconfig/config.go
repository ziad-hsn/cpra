// Package runtimeconfig loads process settings independently of monitor manifests.
package runtimeconfig

import (
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Storage struct {
	Mode             string        `yaml:"mode"`
	Directory        string        `yaml:"directory"`
	BatchDelay       time.Duration `yaml:"batch_delay"`
	BatchSize        int           `yaml:"batch_size"`
	SnapshotInterval time.Duration `yaml:"snapshot_interval"`
	SnapshotRetain   int           `yaml:"snapshot_retain"`
}

type History struct {
	RetentionDays int `yaml:"retention_days"`
}

type SLO struct {
	QueueTarget        time.Duration `yaml:"queue_target"`
	ResultTarget       time.Duration `yaml:"result_target"`
	Window             time.Duration `yaml:"window"`
	ControlWindow      time.Duration `yaml:"control_window"`
	EvaluationInterval time.Duration `yaml:"evaluation_interval"`
	MinimumSamples     uint64        `yaml:"minimum_samples"`
	HealthyHold        time.Duration `yaml:"healthy_hold"`
}

type Config struct {
	Storage Storage `yaml:"storage"`
	History History `yaml:"history"`
	SLO     SLO     `yaml:"slo"`
}

func Default() Config {
	return Config{
		Storage: Storage{Mode: "raft", Directory: "./cpra-data", BatchDelay: 5 * time.Millisecond, BatchSize: 1000, SnapshotInterval: 5 * time.Minute, SnapshotRetain: 3},
		History: History{RetentionDays: 30},
		SLO:     SLO{QueueTarget: 250 * time.Millisecond, ResultTarget: 5 * time.Second, Window: 5 * time.Minute, ControlWindow: 30 * time.Second, EvaluationInterval: 5 * time.Second, MinimumSamples: 1000, HealthyHold: 60 * time.Second},
	}
}

func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open runtime configuration: %w", err)
	}
	defer f.Close()
	d := yaml.NewDecoder(io.LimitReader(f, 1<<20))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("runtime configuration: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("runtime configuration must contain one document")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	s := c.Storage
	if s.Mode != "raft" && s.Mode != "memory" {
		return fmt.Errorf("storage.mode must be raft or memory")
	}
	if s.Mode == "raft" && s.Directory == "" {
		return fmt.Errorf("storage.directory is required in raft mode")
	}
	if s.BatchSize < 1 || s.BatchSize > 1000 || s.BatchDelay <= 0 || s.BatchDelay > 5*time.Millisecond {
		return fmt.Errorf("storage batches require 1..1000 commands and a delay of at most 5ms")
	}
	if s.SnapshotInterval < time.Second || s.SnapshotRetain < 1 {
		return fmt.Errorf("invalid snapshot interval or retention")
	}
	if c.History.RetentionDays != 30 {
		return fmt.Errorf("history.retention_days must be 30 for this storage format")
	}
	p := c.SLO
	if p.QueueTarget <= 0 || p.ResultTarget <= p.QueueTarget || p.Window != 5*time.Minute || p.ControlWindow != 30*time.Second || p.EvaluationInterval != 5*time.Second || p.MinimumSamples < 1000 || p.HealthyHold < 60*time.Second {
		return fmt.Errorf("invalid SLO settings: use a 5m report window, 30s control window, 5s evaluation, at least 1000 samples and 60s healthy hold")
	}
	return nil
}
