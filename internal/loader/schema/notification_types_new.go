package schema

import "strings"

// New code notification (alert delivery) types added on top of the general
// SDK. Config structs always compile (no SDK dependency); only the job
// implementations are build-tagged.

// CodeNotificationTeams delivers alerts to Microsoft Teams via a Workflow /
// Power Automate incoming webhook (O365 connectors are retired).
type CodeNotificationTeams struct {
	WebhookURL string `yaml:"webhook_url" json:"webhook_url"`
}

func (c *CodeNotificationTeams) Copy() CodeNotification {
	return &CodeNotificationTeams{WebhookURL: strings.Clone(c.WebhookURL)}
}

func (c *CodeNotificationTeams) IsCodeNotification() {}

// CodeNotificationMattermost delivers alerts to a Mattermost incoming webhook
// (Slack-compatible payload). Pure stdlib, always compiled.
type CodeNotificationMattermost struct {
	WebhookURL string `yaml:"webhook_url" json:"webhook_url"`
	Channel    string `yaml:"channel" json:"channel"`
	Username   string `yaml:"username" json:"username"`
}

func (c *CodeNotificationMattermost) Copy() CodeNotification {
	return &CodeNotificationMattermost{
		WebhookURL: strings.Clone(c.WebhookURL),
		Channel:    strings.Clone(c.Channel),
		Username:   strings.Clone(c.Username),
	}
}

func (c *CodeNotificationMattermost) IsCodeNotification() {}

// CodeNotificationPushover delivers alerts to a Pushover user/group via the
// push notification API.
type CodeNotificationPushover struct {
	AppToken string `yaml:"app_token" json:"app_token"`
	UserKey  string `yaml:"user_key" json:"user_key"`
	Title    string `yaml:"title" json:"title"`
	Priority int    `yaml:"priority" json:"priority"`                 // -2..2; 2 = emergency
	Retry    int    `yaml:"retry,omitempty" json:"retry,omitempty"`   // Emergency repeat interval in seconds.
	Expire   int    `yaml:"expire,omitempty" json:"expire,omitempty"` // Emergency repeat duration in seconds.
	Sound    string `yaml:"sound" json:"sound"`
}

func (c *CodeNotificationPushover) Copy() CodeNotification {
	return &CodeNotificationPushover{
		AppToken: strings.Clone(c.AppToken),
		UserKey:  strings.Clone(c.UserKey),
		Title:    strings.Clone(c.Title),
		Priority: c.Priority,
		Retry:    c.Retry,
		Expire:   c.Expire,
		Sound:    strings.Clone(c.Sound),
	}
}

func (c *CodeNotificationPushover) IsCodeNotification() {}

// CodeNotificationTwilio delivers alerts as SMS via the Twilio API.
type CodeNotificationTwilio struct {
	AccountSID string `yaml:"account_sid" json:"account_sid"`
	AuthToken  string `yaml:"auth_token" json:"auth_token"`
	From       string `yaml:"from" json:"from"`
	To         string `yaml:"to" json:"to"`
}

func (c *CodeNotificationTwilio) Copy() CodeNotification {
	return &CodeNotificationTwilio{
		AccountSID: strings.Clone(c.AccountSID),
		AuthToken:  strings.Clone(c.AuthToken),
		From:       strings.Clone(c.From),
		To:         strings.Clone(c.To),
	}
}

func (c *CodeNotificationTwilio) IsCodeNotification() {}

// CodeNotificationDatadog posts events to the Datadog events API.
type CodeNotificationDatadog struct {
	APIKey string   `yaml:"api_key" json:"api_key"`
	AppKey string   `yaml:"app_key" json:"app_key"`
	Site   string   `yaml:"site" json:"site"` // datadoghq.com | datadoghq.eu | ...
	Tags   []string `yaml:"tags" json:"tags"`
}

func (c *CodeNotificationDatadog) Copy() CodeNotification {
	return &CodeNotificationDatadog{
		APIKey: strings.Clone(c.APIKey),
		AppKey: strings.Clone(c.AppKey),
		Site:   strings.Clone(c.Site),
		Tags:   append([]string(nil), c.Tags...),
	}
}

func (c *CodeNotificationDatadog) IsCodeNotification() {}

// CodeNotificationVictorOps delivers alerts to Splunk On-Call (VictorOps) via
// the generic REST endpoint. Pure stdlib, always compiled.
type CodeNotificationVictorOps struct {
	RestEndpointKey string `yaml:"rest_endpoint_key" json:"rest_endpoint_key"`
	RoutingKey      string `yaml:"routing_key" json:"routing_key"`
	MessageType     string `yaml:"message_type" json:"message_type"` // CRITICAL | ACKNOWLEDGEMENT | RECOVERY
	EntityID        string `yaml:"entity_id" json:"entity_id"`
}

func (c *CodeNotificationVictorOps) Copy() CodeNotification {
	return &CodeNotificationVictorOps{
		RestEndpointKey: strings.Clone(c.RestEndpointKey),
		RoutingKey:      strings.Clone(c.RoutingKey),
		MessageType:     strings.Clone(c.MessageType),
		EntityID:        strings.Clone(c.EntityID),
	}
}

func (c *CodeNotificationVictorOps) IsCodeNotification() {}
