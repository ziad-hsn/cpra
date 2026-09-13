package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
)

type mapping struct {
	TargetGroupARN   string    `json:"targetGroupARN,omitempty"`
	LoadBalancerName string    `json:"loadBalancerName,omitempty"`
	TargetID         string    `json:"targetID"`
	Port             *int32    `json:"port,omitempty"`
	AvailabilityZone string    `json:"availabilityZone,omitempty"`
	MonitorID        string    `json:"monitorID"`
	MonitorUID       string    `json:"monitorUID"`
	ResourceVersion  string    `json:"resourceVersion"`
	EventsAfter      time.Time `json:"eventsAfter"`
}

type settings struct {
	Account  string    `json:"account"`
	Region   string    `json:"region"`
	Mappings []mapping `json:"mappings"`
}

func readSettings(r io.Reader) (settings, error) {
	var cfg settings
	raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return cfg, errors.New("mapping cannot be read within 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode mapping: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return cfg, errors.New("mapping contains trailing data or exceeds 1 MiB")
	}
	if len(cfg.Account) != 12 || strings.Trim(cfg.Account, "0123456789") != "" || cfg.Region == "" || len(cfg.Mappings) == 0 || len(cfg.Mappings) > 1000 {
		return cfg, errors.New("mapping requires AWS account, region, and 1–1000 entries")
	}
	seen := make(map[string]bool)
	monitors := make(map[string]bool)
	for _, m := range cfg.Mappings {
		if (m.TargetGroupARN == "") == (m.LoadBalancerName == "") || m.TargetID == "" || m.MonitorID == "" || m.MonitorUID == "" || m.ResourceVersion == "" || m.EventsAfter.IsZero() {
			return cfg, errors.New("each mapping needs one AWS owner, target, monitor ID, UID, version, and eventsAfter")
		}
		if m.Port != nil && (*m.Port < 1 || *m.Port > 65535) || m.LoadBalancerName != "" && (m.Port != nil || m.AvailabilityZone != "") {
			return cfg, errors.New("invalid mapping target port or Classic target parameters")
		}
		if m.TargetGroupARN != "" {
			a, err := arn.Parse(m.TargetGroupARN)
			if err != nil || a.Service != "elasticloadbalancing" || a.AccountID != cfg.Account || a.Region != cfg.Region || !strings.HasPrefix(a.Resource, "targetgroup/") {
				return cfg, errors.New("target group ARN is outside the configured account or region")
			}
		}
		key := m.key()
		if seen[key] || monitors[m.MonitorID] {
			return cfg, errors.New("mapping duplicates an AWS target or monitor")
		}
		seen[key], monitors[m.MonitorID] = true, true
	}
	return cfg, nil
}

func (m mapping) key() string {
	p := "default"
	if m.Port != nil {
		p = fmt.Sprint(*m.Port)
	}
	return strings.Join([]string{m.TargetGroupARN, m.LoadBalancerName, m.TargetID, p, m.AvailabilityZone}, "\x00")
}

type registrationReader interface {
	absent(context.Context, mapping) (bool, error)
}

type processor struct {
	cfg    settings
	cpra   *cpra.Client
	aws    registrationReader
	output io.Writer
}

func (p *processor) process(ctx context.Context, body string) error {
	e, err := decodeEvent(body)
	if err != nil {
		return err
	}
	if !e.isDeregistration() || e.Detail.ErrorCode != "" || e.Detail.ErrorMessage != "" {
		return nil // Other events and rejected AWS calls have no disable effect.
	}
	if err := e.validate(p.cfg.Account, p.cfg.Region); err != nil {
		return err
	}
	for _, m := range p.cfg.Mappings {
		if e.Detail.EventTime.Before(m.EventsAfter) || !matches(e, m) {
			continue
		}
		if err := p.disable(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func matches(e event, m mapping) bool {
	p := e.Detail.RequestParameters
	if e.Detail.EventName == "DeregisterInstancesFromLoadBalancer" && m.LoadBalancerName == p.LoadBalancerName {
		for _, i := range p.Instances {
			if i.ID == m.TargetID {
				return true
			}
		}
	}
	if e.Detail.EventName == "DeregisterTargets" && m.TargetGroupARN == p.TargetGroupARN {
		for _, t := range p.Targets {
			portMatches := t.Port == nil && m.Port == nil || t.Port != nil && m.Port != nil && *t.Port == *m.Port
			if t.ID == m.TargetID && portMatches && t.AvailabilityZone == m.AvailabilityZone {
				return true
			}
		}
	}
	return false
}

func (p *processor) disable(ctx context.Context, m mapping) error {
	monitor, err := p.cpra.Monitors.Get(ctx, m.MonitorID)
	if errors.Is(err, cpra.ErrNotFound) {
		fmt.Fprintf(p.output, "monitor %s does not exist; no change\n", m.MonitorID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read mapped monitor: %w", err)
	}
	if monitor.Data.Metadata.UID != m.MonitorUID {
		return errors.New("mapped monitor incarnation changed; review mapping")
	}
	if monitor.Data.Spec.Enabled != nil && !*monitor.Data.Spec.Enabled {
		fmt.Fprintf(p.output, "monitor %s is already disabled; no change\n", m.MonitorID)
		return nil
	}
	if monitor.Data.Metadata.ResourceVersion != m.ResourceVersion {
		return errors.New("mapped monitor version changed; review mapping")
	}
	absent, err := p.aws.absent(ctx, m)
	if err != nil {
		return fmt.Errorf("confirm target registration: %w", err)
	}
	if !absent {
		// Registration reads can lag the successful API event. A later queue
		// delivery rechecks; persistent registration belongs in dead-letter review.
		return errors.New("target remains registered; retry confirmation or review dead-letter event")
	}
	_, err = p.cpra.Monitors.Disable(ctx, m.MonitorID, m.ResourceVersion)
	if err != nil {
		// A conflict or lost reply does not authorize a new mutation with a newer version.
		return fmt.Errorf("disable mapped monitor: %w", err)
	}
	fmt.Fprintf(p.output, "disabled monitor %s after AWS registration confirmation\n", m.MonitorID)
	return nil
}
