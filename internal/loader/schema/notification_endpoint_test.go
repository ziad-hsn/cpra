package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNotificationEndpointOptionsRoundTrip(t *testing.T) {
	for _, kind := range []string{"telegram", "victorops", "pushover", "datadog", "twilio"} {
		t.Run(kind, func(t *testing.T) {
			var endpoint Endpoint
			input := []byte("type: " + kind + "\nconfig:\n  url: http://127.0.0.1:9876/operation\n")
			if err := yaml.Unmarshal(input, &endpoint); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(endpoint.Config)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]interface{}
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if fields["url"] != "http://127.0.0.1:9876/operation" {
				t.Error("endpoint URL lost in decoding")
			}
			if !reflect.DeepEqual(endpoint.Config, endpoint.Config.Copy()) {
				t.Error("copy changed endpoint config")
			}
			encoded, err = json.Marshal(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Endpoint
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(endpoint, decoded) {
				t.Error("JSON round trip changed endpoint config")
			}
		})
	}
	var endpoint Endpoint
	if err := yaml.Unmarshal([]byte("type: telegram\nconfig:\n  bot_token: fixture\n  test_mode: true\n"), &endpoint); err != nil {
		t.Fatal(err)
	}
	if config := endpoint.Config.Copy().(*CodeNotificationTelegram); !config.TestMode || config.BotToken != "fixture" || config.URL != "" {
		t.Error("Telegram test selection lost in copy")
	}
}
