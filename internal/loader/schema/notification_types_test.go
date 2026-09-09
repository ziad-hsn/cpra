package schema

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEndpointNewNotificationTypesDecode(t *testing.T) {
	data := []byte(`
endpoints:
  tg:
    type: telegram
    config:
      bot_token: "123:abc"
      chat_id: "456"
  dc:
    type: discord
    config:
      webhook_url: "https://discord.com/api/webhooks/x"
  og:
    type: opsgenie
    config:
      api_key: "genie-key"
`)

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got %d", len(m.Endpoints))
	}

	if tg, ok := m.Endpoints["tg"].Config.(*CodeNotificationTelegram); !ok {
		t.Fatalf("expected telegram, got %T", m.Endpoints["tg"].Config)
	} else if tg.BotToken != "123:abc" || tg.ChatID != "456" {
		t.Fatalf("telegram fields wrong: %+v", tg)
	}

	if dc, ok := m.Endpoints["dc"].Config.(*CodeNotificationDiscord); !ok {
		t.Fatalf("expected discord, got %T", m.Endpoints["dc"].Config)
	} else if dc.WebhookURL == "" {
		t.Fatal("discord webhook_url empty")
	}

	if og, ok := m.Endpoints["og"].Config.(*CodeNotificationOpsgenie); !ok {
		t.Fatalf("expected opsgenie, got %T", m.Endpoints["og"].Config)
	} else if og.APIKey != "genie-key" {
		t.Fatalf("opsgenie api_key wrong: %+v", og)
	}
}
