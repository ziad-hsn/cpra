package management

import (
	"bytes"
	"encoding/json"
	"time"
)

// comparableSpec normalizes only declared duration fields in a disposable
// comparison copy. Uploaded bytes, commitments and saved resources are never
// rewritten. Omission, null and explicit zero remain distinct. In particular,
// credentials, custom worker parameters and arbitrary string values are opaque.
func comparableSpec(kind string, raw json.RawMessage) json.RawMessage {
	var spec map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&spec) != nil || spec == nil {
		return bytes.Clone(raw)
	}
	object := func(value any) map[string]any { m, _ := value.(map[string]any); return m }
	duration := func(fields map[string]any, keys ...string) {
		for _, key := range keys {
			if value, ok := fields[key].(string); ok {
				if d, err := time.ParseDuration(value); err == nil {
					fields[key] = d.String()
				}
			}
		}
	}
	driver := func(category string, value any) {
		d := object(value)
		config := object(d["config"])
		switch category + "/" + stringValue(d["type"]) {
		case "recovery/docker":
			duration(config, "timeout", "operationTimeout")
		case "recovery/webhook", "recovery/systemd", "recovery/aws", "recovery/kubernetes":
			duration(config, "timeout")
		case "recovery/a2a":
			duration(config, "requestTimeout", "taskTimeout", "pollInterval")
		case "notification/a2a":
			duration(config, "requestTimeout", "timeout")
		}
	}
	switch kind {
	case "Monitor":
		duration(object(spec["check"]), "interval", "timeout")
		duration(object(spec["autoIgnore"]), "window")
		recovery := object(spec["recovery"])
		duration(recovery, "cooldown")
		driver("recovery", recovery["driver"])
		if rules, ok := recovery["rules"].([]any); ok {
			for _, rule := range rules {
				driver("recovery", object(rule)["driver"])
			}
		}
		for _, rule := range object(spec["notifications"]) {
			driver("notification", object(rule)["driver"])
		}
	case "NotificationEndpoint":
		driver("notification", spec)
	}
	result, err := json.Marshal(spec)
	if err != nil {
		return bytes.Clone(raw)
	}
	return result
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func desiredSpecEqual(kind string, a, b json.RawMessage) bool {
	left, right := comparableSpec(kind, a), comparableSpec(kind, b)
	defer clear(left)
	defer clear(right)
	return jsonEqual(left, right)
}
