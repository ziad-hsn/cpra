package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	cprav1 "cpra/gen/cpra/v1"
	"cpra/cmd/cpractl/output"
	"cpra/internal/platform/loader/schema"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/types/known/durationpb"
)

func loadMonitorsFromFile(path string) ([]*cprav1.Monitor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var manifest schema.Manifest
	if err := yaml.Unmarshal(data, &manifest); err == nil && len(manifest.Monitors) > 0 {
		for i := range manifest.Monitors {
			schema.DefaultConfig.Apply(&manifest.Monitors[i])
		}
		if err := schema.ValidateManifest(&manifest); err != nil {
			return nil, err
		}
		return convertMonitors(manifest.Monitors), nil
	}

	var single schema.Monitor
	if err := yaml.Unmarshal(data, &single); err == nil && single.Name != "" {
		schema.DefaultConfig.Apply(&single)
		if err := schema.ValidateMonitor(&single); err != nil {
			return nil, err
		}
		return []*cprav1.Monitor{monitorToProto(&single)}, nil
	}

	var proto cprav1.Monitor
	if err := yaml.Unmarshal(data, &proto); err == nil && proto.GetName() != "" {
		return []*cprav1.Monitor{&proto}, nil
	}

	return nil, fmt.Errorf("unsupported monitor YAML format")
}

func convertMonitors(monitors []schema.Monitor) []*cprav1.Monitor {
	out := make([]*cprav1.Monitor, 0, len(monitors))
	for i := range monitors {
		out = append(out, monitorToProto(&monitors[i]))
	}
	return out
}

func formatMonitorsOutput(monitors []*cprav1.Monitor, format string, f output.Formatter) error {
	if format == "table" || format == "wide" {
		return f.Format(monitorsTable(monitors))
	}
	if len(monitors) == 1 {
		return f.Format(monitors[0])
	}
	return f.Format(monitors)
}

func monitorsTable(monitors []*cprav1.Monitor) output.TableData {
	rows := make([][]string, 0, len(monitors))
	for _, m := range monitors {
		interval := ""
		pulseType := ""
		if m.GetPulse() != nil {
			pulseType = m.GetPulse().GetType()
			if m.GetPulse().GetInterval() != nil {
				interval = m.GetPulse().GetInterval().AsDuration().String()
			}
		}
		rows = append(rows, []string{
			m.GetName(),
			pulseType,
			fmt.Sprintf("%t", m.GetEnabled()),
			interval,
		})
	}
	return output.TableData{
		Headers: []string{"NAME", "TYPE", "ENABLED", "INTERVAL"},
		Rows:    rows,
	}
}

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

	pb.Pulse = &cprav1.Pulse{
		Type:               m.Pulse.Type,
		Interval:           durationpb.New(m.Pulse.Interval),
		Timeout:            durationpb.New(m.Pulse.Timeout),
		MaxFailures:        int32(m.Pulse.MaxFailures),
		UnhealthyThreshold: int32(m.Pulse.UnhealthyThreshold),
		HealthyThreshold:   int32(m.Pulse.HealthyThreshold),
		Groups:             m.Pulse.Groups,
	}

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
