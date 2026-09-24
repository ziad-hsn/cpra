package api

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestStrictDecode(t *testing.T) {
	for _, s := range []string{`{"enabled":false,"enabled":true}`, `{"extra":1}`, `{} {}`, `{"check":{"driver":{"type":"http","config":{"url":"x","url":"y"}}}}`} {
		var out MonitorSpec
		if StrictDecode([]byte(s), &out) == nil {
			t.Fatal("accepted", s)
		}
	}
}
func TestOptionalPresence(t *testing.T) {
	for _, s := range []string{`{}`, `{"enabled":false}`, `{"enabled":true}`} {
		var out MonitorSpec
		if e := json.Unmarshal([]byte(s), &out); e != nil {
			t.Fatal(e)
		}
		if s == `{}` && out.Enabled != nil {
			t.Fatal("omission default materialized")
		}
		if s == `{"enabled":false}` && (out.Enabled == nil || *out.Enabled) {
			t.Fatal("false lost")
		}
	}
}
func TestUnknownDriverReadableNotWritable(t *testing.T) {
	var d DriverConfig
	if e := json.Unmarshal([]byte(`{"type":"future","config":{"x":false}}`), &d); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(ValidateDriver("check", d), ErrUnsupportedDriver) {
		t.Fatal("unknown writable")
	}
}
func TestEveryBuiltInDriverRoundTrips(t *testing.T) {
	types := map[string][]string{"check": {"http", "tcp", "icmp", "dns", "udp", "grpc", "docker", "redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka", "tls"}, "recovery": {"docker", "kubernetes", "webhook", "systemd", "aws"}, "notification": {"log", "pagerduty", "slack", "discord", "email", "webhook", "telegram", "teams", "mattermost", "pushover", "twilio", "datadog", "victorops", "opsgenie"}}
	count := 0
	for category, names := range types {
		for _, name := range names {
			t.Run(category+"/"+name, func(t *testing.T) {
				target := driverTarget(category, name)
				if target == nil {
					t.Fatal("missing concrete schema")
				}
				value := reflect.ValueOf(target).Elem()
				for i := 0; i < value.NumField(); i++ {
					f := value.Field(i)
					if f.Kind() == reflect.Pointer {
						f.Set(reflect.New(f.Type().Elem()))
						switch f.Elem().Kind() {
						case reflect.Map:
							f.Elem().Set(reflect.MakeMap(f.Elem().Type()))
						case reflect.Slice:
							f.Elem().Set(reflect.MakeSlice(f.Elem().Type(), 0, 0))
						}
					}
				}
				raw, e := json.Marshal(target)
				if e != nil {
					t.Fatal(e)
				}
				d := DriverConfig{Type: name, Config: raw}
				if e := ValidateDriver(category, d); e != nil {
					t.Fatal(e)
				}
				copy := driverTarget(category, name)
				if e := json.Unmarshal(raw, copy); e != nil {
					t.Fatal(e)
				}
				again, _ := json.Marshal(copy)
				if string(again) != string(raw) {
					t.Fatal("round trip changed presence", string(raw), string(again))
				}
			})
			count++
		}
	}
	if count != 33 {
		t.Fatal(count)
	}
}
func TestManifestNormalizationDoesNotRewriteHeaders(t *testing.T) {
	out, e := NormalizeManifestDriver("check", "http", json.RawMessage(`{"insecure_skip_verify":false,"headers":{"X-Custom_Header":"v"}}`))
	if e != nil {
		t.Fatal(e)
	}
	var d PulseHTTPConfig
	if e := json.Unmarshal(out, &d); e != nil {
		t.Fatal(e)
	}
	if d.InsecureSkipVerify == nil || *d.InsecureSkipVerify || (*d.Headers)["X-Custom_Header"] != "v" {
		t.Fatal(string(out))
	}
}

func TestMutableSpecPreservesZeroAndEmpty(t *testing.T) {
	absent := Monitor{APIVersion: APIVersion, Kind: "Monitor", Metadata: Metadata{ID: "m"}, Spec: MonitorSpec{}}
	explicit := absent
	explicit.Metadata.Labels = Pointer(map[string]string{})
	explicit.Spec.Check.Retries = Pointer(int64(0))
	explicit.Spec.Check.HealthyThreshold = Pointer(int64(0))
	explicit.Spec.Check.Groups = Pointer([]string{})
	explicit.Spec.Check.Driver.CredentialRefs = Pointer(map[string]string{})
	explicit.Spec.Enabled = Pointer(false)
	for name, value := range map[string]Monitor{"absent": absent, "explicit": explicit} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Monitor
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if name == "explicit" {
			if decoded.Metadata.Labels == nil || decoded.Spec.Check.Retries == nil || *decoded.Spec.Check.Retries != 0 || decoded.Spec.Check.Groups == nil || decoded.Spec.Enabled == nil || *decoded.Spec.Enabled {
				t.Fatal("explicit zero/empty lost", string(raw))
			}
		}
		if name == "absent" {
			if decoded.Metadata.Labels != nil || decoded.Spec.Check.Retries != nil || decoded.Spec.Check.Groups != nil || decoded.Spec.Enabled != nil {
				t.Fatal("omitted value materialized", string(raw))
			}
		}
	}
}
