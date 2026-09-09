package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Endpoint is a reusable delivery endpoint (email, webhook, slack, pagerduty,
// telegram, discord, opsgenie, log). It is referenced by name from notification
// groups, so the same endpoint can be shared across many monitors without
// duplicating its configuration.
type Endpoint struct {
	Type   string           `yaml:"type" json:"type"`
	Config CodeNotification `yaml:"config" json:"config"`
}

// UnmarshalYAML decodes an Endpoint, resolving the polymorphic Config by Type.
func (e *Endpoint) UnmarshalYAML(value *yaml.Node) error {
	var temp struct {
		Type   string    `yaml:"type"`
		Config yaml.Node `yaml:"config"`
	}
	if err := value.Decode(&temp); err != nil {
		return err
	}
	cfg, err := decodeCodeNotificationYAML(temp.Type, temp.Config)
	if err != nil {
		return err
	}
	e.Type = temp.Type
	e.Config = cfg
	return nil
}

// UnmarshalJSON decodes an Endpoint, resolving the polymorphic Config by Type.
func (e *Endpoint) UnmarshalJSON(data []byte) error {
	var temp struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}
	cfg, err := decodeCodeNotificationJSON(temp.Type, temp.Config)
	if err != nil {
		return err
	}
	e.Type = temp.Type
	e.Config = cfg
	return nil
}

// NotificationGroups maps a group name to an ordered list of endpoint names.
// A monitor alert rule references a group by name via CodeConfig.NotifyGroup.
type NotificationGroups map[string][]string

// MaintenanceWindow defines a scheduled downtime during which alerts for the
// monitor are suppressed.
type MaintenanceWindow struct {
	Cron     string `yaml:"cron" json:"cron"`
	Duration string `yaml:"duration" json:"duration"`
	Timezone string `yaml:"timezone" json:"timezone"`
}

// CodeNotificationEmail delivers alerts via SMTP email.
type CodeNotificationEmail struct {
	AllowInsecure bool   `yaml:"allow_insecure" json:"allow_insecure"`
	To            string `yaml:"to" json:"to"`
	From          string `yaml:"from" json:"from"`
	Server        string `yaml:"server" json:"server"`
	Subject       string `yaml:"subject" json:"subject"`
}

func (c *CodeNotificationEmail) Copy() CodeNotification {
	return &CodeNotificationEmail{
		AllowInsecure: c.AllowInsecure,
		To:            strings.Clone(c.To),
		From:          strings.Clone(c.From),
		Server:        strings.Clone(c.Server),
		Subject:       strings.Clone(c.Subject),
	}
}

func (c *CodeNotificationEmail) IsCodeNotification() {}

// CodeNotificationWebhook delivers alerts to an HTTP webhook endpoint.
type CodeNotificationWebhook struct {
	URL     string            `yaml:"url" json:"url"`
	Method  string            `yaml:"method" json:"method"`
	Headers map[string]string `yaml:"headers" json:"headers"`
}

func (c *CodeNotificationWebhook) Copy() CodeNotification {
	headers := make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		headers[k] = v
	}
	return &CodeNotificationWebhook{
		URL:     strings.Clone(c.URL),
		Method:  strings.Clone(c.Method),
		Headers: headers,
	}
}

func (c *CodeNotificationWebhook) IsCodeNotification() {}

// decodeCodeNotificationYAML resolves a notification config node to a concrete
// CodeNotification based on the notify type. This is the single YAML decode
// helper shared by Codes and Endpoint.
func decodeCodeNotificationYAML(notifyType string, node yaml.Node) (CodeNotification, error) {
	switch notifyType {
	case "log":
		var t CodeNotificationLog
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "slack":
		var t CodeNotificationSlack
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "pagerduty":
		var t CodeNotificationPagerDuty
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "email":
		var t CodeNotificationEmail
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "webhook":
		var t CodeNotificationWebhook
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "telegram":
		var t CodeNotificationTelegram
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "discord":
		var t CodeNotificationDiscord
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "opsgenie":
		var t CodeNotificationOpsgenie
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "teams":
		var t CodeNotificationTeams
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "mattermost":
		var t CodeNotificationMattermost
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "pushover":
		var t CodeNotificationPushover
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "twilio":
		var t CodeNotificationTwilio
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "datadog":
		var t CodeNotificationDatadog
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case "victorops":
		var t CodeNotificationVictorOps
		if err := node.Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	default:
		return nil, fmt.Errorf("unknown notification type: %q", notifyType)
	}
}

// decodeCodeNotificationJSON resolves a notification config raw message to a
// concrete CodeNotification based on the notify type. This is the single JSON
// decode helper shared by Codes and Endpoint.
func decodeCodeNotificationJSON(notifyType string, raw json.RawMessage) (CodeNotification, error) {
	switch notifyType {
	case "log":
		var t CodeNotificationLog
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "slack":
		var t CodeNotificationSlack
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "pagerduty":
		var t CodeNotificationPagerDuty
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "email":
		var t CodeNotificationEmail
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "webhook":
		var t CodeNotificationWebhook
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "telegram":
		var t CodeNotificationTelegram
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "discord":
		var t CodeNotificationDiscord
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "opsgenie":
		var t CodeNotificationOpsgenie
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "teams":
		var t CodeNotificationTeams
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "mattermost":
		var t CodeNotificationMattermost
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "pushover":
		var t CodeNotificationPushover
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "twilio":
		var t CodeNotificationTwilio
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "datadog":
		var t CodeNotificationDatadog
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "victorops":
		var t CodeNotificationVictorOps
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		return &t, nil
	default:
		return nil, fmt.Errorf("unknown notification type: %q", notifyType)
	}
}
