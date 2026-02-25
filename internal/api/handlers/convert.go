package handlers

import (
	"encoding/json"
	"fmt"
	"time"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/internal/platform/loader/schema"

	"google.golang.org/protobuf/types/known/durationpb"
)

// protoToMonitor converts a proto Monitor to schema.Monitor.
func protoToMonitor(pb *cprav1.Monitor) (*schema.Monitor, error) {
	if pb == nil {
		return nil, fmt.Errorf("monitor cannot be nil")
	}

	m := &schema.Monitor{
		ID:      pb.GetId(),
		Name:    pb.GetName(),
		Slug:    pb.GetSlug(),
		Enabled: pb.GetEnabled(),
	}

	if pb.Metadata != nil && len(pb.Metadata) > 0 {
		var metadata any
		if err := json.Unmarshal(pb.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("invalid metadata JSON: %w", err)
		}
		m.Metadata = metadata
	}

	// Convert pulse
	if pbPulse := pb.GetPulse(); pbPulse != nil {
		interval := time.Duration(0)
		if pbPulse.GetInterval() != nil {
			interval = pbPulse.GetInterval().AsDuration()
		}
		timeout := time.Duration(0)
		if pbPulse.GetTimeout() != nil {
			timeout = pbPulse.GetTimeout().AsDuration()
		}
		m.Pulse = schema.Pulse{
			Type:               pbPulse.GetType(),
			Interval:           interval,
			Timeout:            timeout,
			MaxFailures:        int(pbPulse.GetMaxFailures()),
			UnhealthyThreshold: int(pbPulse.GetUnhealthyThreshold()),
			HealthyThreshold:   int(pbPulse.GetHealthyThreshold()),
			Groups:             pbPulse.GetGroups(),
		}

		// Convert pulse config based on type
		switch cfg := pbPulse.GetConfig().(type) {
		case *cprav1.Pulse_Http:
			m.Pulse.Config = &schema.PulseHTTPConfig{
				Url:     cfg.Http.GetUrl(),
				Method:  cfg.Http.GetMethod(),
				Headers: cfg.Http.GetHeaders(),
				Retries: int(cfg.Http.GetRetries()),
			}
		case *cprav1.Pulse_Tcp:
			m.Pulse.Config = &schema.PulseTCPConfig{
				Host:    cfg.Tcp.GetHost(),
				Port:    int(cfg.Tcp.GetPort()),
				Retries: int(cfg.Tcp.GetRetries()),
			}
		case *cprav1.Pulse_Icmp:
			m.Pulse.Config = &schema.PulseICMPConfig{
				Host:      cfg.Icmp.GetHost(),
				Count:     int(cfg.Icmp.GetCount()),
				Privilege: cfg.Icmp.GetPrivilege(),
				Retries:   int(cfg.Icmp.GetRetries()),
			}
		}
	}

	// Convert codes
	if pbCodes := pb.GetCodes(); len(pbCodes) > 0 {
		m.Codes = make(schema.Codes, len(pbCodes))
		for color, pbCode := range pbCodes {
			codeConfig := schema.CodeConfig{
				Notify:   pbCode.GetNotify(),
				Dispatch: pbCode.GetDispatch(),
			}

			switch cfg := pbCode.GetConfig().(type) {
			case *cprav1.CodeConfig_Log:
				codeConfig.Config = &schema.CodeNotificationLog{
					File: cfg.Log.GetFile(),
				}
			case *cprav1.CodeConfig_Slack:
				webhook := cfg.Slack.GetWebhook()
				if webhook == "" {
					webhook = cfg.Slack.GetWebhookUrl()
				}
				codeConfig.Config = &schema.CodeNotificationSlack{
					WebHook:    webhook,
					WebHookURL: cfg.Slack.GetWebhookUrl(),
					Channel:    cfg.Slack.GetChannel(),
				}
			case *cprav1.CodeConfig_Pagerduty:
				codeConfig.Config = &schema.CodeNotificationPagerDuty{
					IntegrationKey: cfg.Pagerduty.GetIntegrationKey(),
					URL:            cfg.Pagerduty.GetUrl(),
					Severity:       cfg.Pagerduty.GetSeverity(),
				}
			}

			m.Codes[color] = codeConfig
		}
	}

	// Convert intervention
	if pbIntervention := pb.GetIntervention(); pbIntervention != nil {
		m.Intervention = schema.Intervention{
			Action:      pbIntervention.GetAction(),
			Retries:     int(pbIntervention.GetRetries()),
			MaxFailures: int(pbIntervention.GetMaxFailures()),
		}
		switch target := pbIntervention.GetTarget().(type) {
		case *cprav1.Intervention_Docker:
			if target.Docker != nil {
				timeout := time.Duration(0)
				if target.Docker.GetTimeout() != nil {
					timeout = target.Docker.GetTimeout().AsDuration()
				}
				m.Intervention.Target = &schema.InterventionTargetDocker{
					Type:       target.Docker.GetType(),
					Container:  target.Docker.GetContainer(),
					Service:    target.Docker.GetService(),
					Replicas:   target.Docker.GetReplicas(),
					Signal:     target.Docker.GetSignal(),
					DockerHost: target.Docker.GetDockerHost(),
					Timeout:    timeout,
				}
			}
		}
	}

	return m, nil
}

// monitorToProto converts a schema.Monitor to proto Monitor.
func monitorToProto(m *schema.Monitor) *cprav1.Monitor {
	if m == nil {
		return nil
	}

	pb := &cprav1.Monitor{
		Id:      m.ID,
		Name:    m.Name,
		Slug:    m.Slug,
		Enabled: m.Enabled,
	}

	if metaBytes, ok := marshalMetadata(m.Metadata); ok {
		pb.Metadata = metaBytes
	}

	// Convert pulse
	pb.Pulse = &cprav1.Pulse{
		Type:               m.Pulse.Type,
		Interval:           durationpb.New(m.Pulse.Interval),
		Timeout:            durationpb.New(m.Pulse.Timeout),
		MaxFailures:        int32(m.Pulse.MaxFailures),
		UnhealthyThreshold: int32(m.Pulse.UnhealthyThreshold),
		HealthyThreshold:   int32(m.Pulse.HealthyThreshold),
		Groups:             m.Pulse.Groups,
	}

	// Convert pulse config
	switch cfg := m.Pulse.Config.(type) {
	case *schema.PulseHTTPConfig:
		pb.Pulse.Config = &cprav1.Pulse_Http{
			Http: &cprav1.PulseHTTPConfig{
				Url:     cfg.Url,
				Method:  cfg.Method,
				Headers: cfg.Headers,
				Retries: int32(cfg.Retries),
			},
		}
	case *schema.PulseTCPConfig:
		pb.Pulse.Config = &cprav1.Pulse_Tcp{
			Tcp: &cprav1.PulseTCPConfig{
				Host:    cfg.Host,
				Port:    int32(cfg.Port),
				Retries: int32(cfg.Retries),
			},
		}
	case *schema.PulseICMPConfig:
		pb.Pulse.Config = &cprav1.Pulse_Icmp{
			Icmp: &cprav1.PulseICMPConfig{
				Host:      cfg.Host,
				Count:     int32(cfg.Count),
				Privilege: cfg.Privilege,
				Retries:   int32(cfg.Retries),
			},
		}
	}

	// Convert codes
	if len(m.Codes) > 0 {
		pb.Codes = make(map[string]*cprav1.CodeConfig, len(m.Codes))
		for color, code := range m.Codes {
			pbCode := &cprav1.CodeConfig{
				Notify:   code.Notify,
				Dispatch: code.Dispatch,
			}

			switch cfg := code.Config.(type) {
			case *schema.CodeNotificationLog:
				pbCode.Config = &cprav1.CodeConfig_Log{
					Log: &cprav1.LogNotification{File: cfg.File},
				}
			case *schema.CodeNotificationSlack:
				webhook := cfg.WebHook
				if webhook == "" {
					webhook = cfg.WebHookURL
				}
				pbCode.Config = &cprav1.CodeConfig_Slack{
					Slack: &cprav1.SlackNotification{
						Webhook:    webhook,
						WebhookUrl: cfg.WebHookURL,
						Channel:    cfg.Channel,
					},
				}
			case *schema.CodeNotificationPagerDuty:
				pbCode.Config = &cprav1.CodeConfig_Pagerduty{
					Pagerduty: &cprav1.PagerDutyNotification{
						IntegrationKey: cfg.IntegrationKey,
						Url:            cfg.URL,
						Severity:       cfg.Severity,
					},
				}
			}

			pb.Codes[color] = pbCode
		}
	}

	// Convert intervention
	if m.Intervention.Action != "" {
		pb.Intervention = &cprav1.Intervention{
			Action:      m.Intervention.Action,
			Retries:     int32(m.Intervention.Retries),
			MaxFailures: int32(m.Intervention.MaxFailures),
		}
		switch target := m.Intervention.Target.(type) {
		case *schema.InterventionTargetDocker:
			pb.Intervention.Target = &cprav1.Intervention_Docker{
				Docker: &cprav1.DockerTarget{
					Type:       target.Type,
					Container:  target.Container,
					Service:    target.Service,
					Replicas:   target.Replicas,
					Signal:     target.Signal,
					DockerHost: target.DockerHost,
					Timeout:    durationpb.New(target.Timeout),
				},
			}
		}
	}

	return pb
}

// Helper to convert time.Duration to protobuf Duration
func durationToProto(d time.Duration) *durationpb.Duration {
	return durationpb.New(d)
}

func marshalMetadata(metadata any) ([]byte, bool) {
	if metadata == nil {
		return nil, false
	}
	switch v := metadata.(type) {
	case []byte:
		if len(v) == 0 {
			return nil, false
		}
		return v, true
	case json.RawMessage:
		if len(v) == 0 {
			return nil, false
		}
		return []byte(v), true
	default:
		data, err := json.Marshal(v)
		if err != nil || len(data) == 0 {
			return nil, false
		}
		return data, true
	}
}
