package collection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// These are wire-format fixtures, not driver executions or provider evidence.
// Every documented built-in variant passes through the production collection
// loader and retains representative optional values and provider parameters.
func TestAll33ManifestDriverVariants(t *testing.T) {
	cases := []struct {
		category, kind, config, key string
		value                       any
	}{
		{"check", "http", `{"url":"https://example.test","expected_status":[200,204],"insecure_skip_verify":false,"headers":{"X_Trace_ID":"keep"}}`, "insecureSkipVerify", false},
		{"check", "tcp", `{"host":"db.internal","port":5432}`, "port", float64(5432)},
		{"check", "icmp", `{"host":"router","ignore_privilege":false}`, "ignorePrivilege", false},
		{"check", "dns", `{"host":"api.internal","server":"10.0.0.53:53"}`, "server", "10.0.0.53:53"},
		{"check", "udp", `{"host":"dns.internal","port":53,"payload":"ping"}`, "payload", "ping"},
		{"check", "grpc", `{"host":"grpc.internal","port":443,"service":"health"}`, "service", "health"},
		{"check", "docker", `{"container":"test-only"}`, "container", "test-only"},
		{"check", "redis", `{"addr":"db:6379","db":0,"password":"fixture-secret"}`, "db", float64(0)},
		{"check", "postgres", `{"host":"db","port":5432,"sslmode":"require","password":"fixture-secret"}`, "sslmode", "require"},
		{"check", "mysql", `{"dsn":"fixture-dsn","password":"fixture-secret"}`, "dsn", "fixture-dsn"},
		{"check", "mongo", `{"uri":"mongodb://db/test"}`, "uri", "mongodb://db/test"},
		{"check", "rabbitmq", `{"url":"amqp://broker/test"}`, "url", "amqp://broker/test"},
		{"check", "kafka", `{"brokers":["broker:9092"]}`, "brokers", []any{"broker:9092"}},
		{"check", "tls", `{"host":"api","server_name":"api.internal","warn_days":14,"critical_days":7,"insecure_skip_verify":false}`, "serverName", "api.internal"},
		{"recovery", "docker", `{"container":"test-only","timeout":"10s","type":"restart"}`, "timeout", "10s"},
		{"recovery", "kubernetes", `{"name":"test-only","namespace":"test","kubeconfig_path":"/config/kube","replicas":0}`, "kubeconfigPath", "/config/kube"},
		{"recovery", "webhook", `{"url":"https://example.test","headers":{"X_Key":"keep"},"timeout":1000000000}`, "timeout", "1s"},
		{"recovery", "systemd", `{"unit":"test-only.service","mode":"replace"}`, "unit", "test-only.service"},
		{"recovery", "aws", `{"instance_id":"fixture-instance","region":"test-region"}`, "instanceId", "fixture-instance"},
		{"notification", "log", `{"file":"/tmp/test-only.log"}`, "file", "/tmp/test-only.log"},
		{"notification", "slack", `{"hook":"https://example.test"}`, "hook", "https://example.test"},
		{"notification", "pagerduty", `{"routing_key":"fixture-key"}`, "routingKey", "fixture-key"},
		{"notification", "email", `{"allow_insecure":false,"to":"test@example.test","server":"smtp:465"}`, "allowInsecure", false},
		{"notification", "webhook", `{"url":"https://example.test","headers":{"X_Key":"keep"}}`, "url", "https://example.test"},
		{"notification", "telegram", `{"bot_token":"fixture-token","chat_id":"fixture-chat","test_mode":false}`, "testMode", false},
		{"notification", "discord", `{"webhook_url":"https://example.test"}`, "webhookUrl", "https://example.test"},
		{"notification", "opsgenie", `{"api_key":"fixture-key"}`, "apiKey", "fixture-key"},
		{"notification", "teams", `{"webhook_url":"https://example.test"}`, "webhookUrl", "https://example.test"},
		{"notification", "mattermost", `{"webhook_url":"https://example.test","channel":"test"}`, "channel", "test"},
		{"notification", "pushover", `{"app_token":"fixture-token","user_key":"fixture-user","priority":0}`, "priority", float64(0)},
		{"notification", "twilio", `{"account_sid":"fixture-account","auth_token":"fixture-token","from":"fixture-from","to":"fixture-to"}`, "accountSid", "fixture-account"},
		{"notification", "datadog", `{"api_key":"fixture-key","app_key":"fixture-app","tags":["test"]}`, "appKey", "fixture-app"},
		{"notification", "victorops", `{"rest_endpoint_key":"fixture-endpoint","routing_key":"fixture-routing","entity_id":"fixture-id"}`, "entityId", "fixture-id"},
	}
	if len(cases) != 33 {
		t.Fatal("variant inventory changed")
	}
	for _, test := range cases {
		t.Run(test.category+"/"+test.kind, func(t *testing.T) {
			check := `"pulse_check":{"type":"http","interval":"60s","timeout":"5s","config":{"url":"https://example.test"}}`
			extra := ""
			switch test.category {
			case "check":
				check = fmt.Sprintf(`"pulse_check":{"type":%q,"interval":"60s","timeout":"5s","max_failures":3,"groups":"pipeline","config":%s}`, test.kind, test.config)
			case "recovery":
				extra = fmt.Sprintf(`,"intervention":{"action":%q,"target":%s,"retries":0,"max_failures":3}`, test.kind, test.config)
			case "notification":
				extra = fmt.Sprintf(`,"codes":{"red":{"dispatch":false,"notify":%q,"config":%s}}`, test.kind, test.config)
			}
			text := `{"version":1,"monitors":[{"id":"test","name":"fixture",` + check + extra + `}]}`
			f := freezeTest(t, text)
			item, err := f.Item(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			var spec api.MonitorSpec
			if err = json.Unmarshal(item.Resource.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			driver := spec.Check.Driver
			if test.category == "recovery" {
				driver = spec.Recovery.Driver
			}
			if test.category == "notification" {
				rule := (*spec.Notifications)["red"]
				if rule.Dispatch == nil || *rule.Dispatch {
					t.Fatal("explicit dispatch false lost")
				}
				driver = *rule.Driver
			}
			if driver.Type != test.kind {
				t.Fatal(driver.Type)
			}
			var config map[string]any
			if err = json.Unmarshal(driver.Config, &config); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(config[test.key])
			want, _ := json.Marshal(test.value)
			if string(got) != string(want) {
				t.Fatalf("%s got %s want %s", test.key, got, want)
			}
			if headers, ok := config["headers"].(map[string]any); ok {
				for key := range headers {
					if !strings.Contains(key, "_") {
						t.Fatal("user map key modified")
					}
				}
			}
			if test.category == "check" && (spec.Check.UnhealthyThreshold == nil || *spec.Check.UnhealthyThreshold != 3 || spec.Check.Groups == nil || len(*spec.Check.Groups) != 1) {
				t.Fatal("manifest default/group normalization lost")
			}
		})
	}
}
