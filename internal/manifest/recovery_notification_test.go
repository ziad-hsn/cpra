package manifest

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestRecoveryNotificationManifestPolicy(t *testing.T) {
	input := []byte(`{"name":"test","pulse_check":{"type":"http","interval":"60s","timeout":"5s","config":{"url":"https://test.example"}},"intervention":{"action":"docker","max_attempts":2,"target":{"container":"designated-test"}},"codes":{"red":{"trigger":"recovery_exhausted","notify":"log","config":{"file":""}}}}`)
	for _, decode := range []func([]byte, any) error{json.Unmarshal, yaml.Unmarshal} {
		var monitor Monitor
		if err := decode(input, &monitor); err != nil {
			t.Fatal(err)
		}
		if err := ValidateMonitor(&monitor); err != nil {
			t.Fatal(err)
		}
		if monitor.Intervention.MaxAttempts != 2 || monitor.Codes["red"].Trigger != "recovery_exhausted" {
			t.Fatal("manifest policy was discarded")
		}
		old, err := ConfigurationRevision(monitor, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		code := monitor.Codes["red"]
		code.Trigger = "recovery_failed"
		monitor.Codes["red"] = code
		next, _ := ConfigurationRevision(monitor, nil, nil)
		if next == old {
			t.Fatal("changed trigger retained old execution identity")
		}
		monitor.Codes = Codes{"yellow": code}
		if ValidateMonitor(&monitor) == nil {
			t.Fatal("recovery trigger accepted outside red")
		}
		monitor.Codes = Codes{"red": code}
		monitor.Intervention = Intervention{}
		if ValidateMonitor(&monitor) == nil {
			t.Fatal("recovery trigger accepted without recovery")
		}
	}
}
