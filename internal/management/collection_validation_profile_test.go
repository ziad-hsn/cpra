package management

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func profileInputs(t *testing.T) (collectionValidationProfileData, []byte, []string, []jobs.Capability, []collectionProfileRuntimeModel) {
	t.Helper()
	models, err := collectionValidationRuntimeModels(runtimeDriverFactories)
	if err != nil {
		t.Fatal(err)
	}
	return collectionValidationProfileDefaults(), api.Schema(), ResourceKinds(), jobs.Capabilities(), models
}

func TestCollectionValidationProfileCanonicalOwned(t *testing.T) {
	base, schemaBytes, resources, capabilities, models := profileInputs(t)
	first, digest, err := buildCollectionValidationProfile(base, schemaBytes, resources, capabilities, models)
	if err != nil {
		t.Fatal(err)
	}
	if first.GOOS != runtime.GOOS || len(first.Drivers) != 33 || len(first.Resources) != 5 {
		t.Fatal("incorrect build inventory")
	}
	originalResources := slices.Clone(resources)
	originalCaps := slices.Clone(capabilities)
	originalModels := slices.Clone(models)
	firstRaw, err := encodeCollectionValidationProfile(first)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(append([]byte("cpra/collection/validation-profile/v1\x00"), firstRaw...))
	schemaHash := sha256.Sum256(schemaBytes)
	if digest != hex.EncodeToString(h[:]) || first.SchemaDigest != hex.EncodeToString(schemaHash[:]) {
		t.Fatal("hash framing or exact schema identity changed")
	}
	for range 20 {
		slices.Reverse(resources)
		slices.Reverse(capabilities)
		slices.Reverse(models)
		again, next, err := buildCollectionValidationProfile(base, schemaBytes, resources, capabilities, models)
		if err != nil || next != digest || !reflect.DeepEqual(again, first) {
			t.Fatal("input ordering changed canonical profile")
		}
	}
	if !slices.Equal(resources, originalResources) || !slices.Equal(capabilities, originalCaps) || !slices.Equal(models, originalModels) {
		t.Fatal("builder mutated borrowed input")
	}
	resources[0] = "caller-mutation"
	capabilities[0].Driver = "caller-mutation"
	models[0].model = "caller-mutation"
	after, _ := encodeCollectionValidationProfile(first)
	if !bytes.Equal(firstRaw, after) {
		t.Fatal("profile aliases caller input")
	}
	for _, forbidden := range []string{"JobType", "ExternalConfig", "hostname", "releaseVersion", "credential", "reason"} {
		if bytes.Contains(firstRaw, []byte(forbidden)) {
			t.Fatalf("unexpected profile field %q", forbidden)
		}
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			got, identity, err := collectionValidationProfile()
			if err != nil || identity != digest || !reflect.DeepEqual(got, first) {
				t.Error("cached profile differs")
				return
			}
			got.Resources[0] = "caller-mutation"
			got.Drivers[0].ConfigModel = "caller-mutation"
		})
	}
	wg.Wait()
	final, identity, err := collectionValidationProfile()
	if err != nil || identity != digest || !reflect.DeepEqual(final, first) {
		t.Fatal("cached slices were mutable")
	}
}

func TestCollectionValidationProfileIdentitySensitivity(t *testing.T) {
	base, contract, resources, capabilities, models := profileInputs(t)
	_, original, err := buildCollectionValidationProfile(base, contract, resources, capabilities, models)
	if err != nil {
		t.Fatal(err)
	}
	changed := func(t *testing.T, base collectionValidationProfileData, contract []byte, kinds []string, caps []jobs.Capability, mappings []collectionProfileRuntimeModel) {
		t.Helper()
		_, digest, err := buildCollectionValidationProfile(base, contract, kinds, caps, mappings)
		if err != nil || digest == original {
			t.Fatalf("semantic input did not change identity: %v", err)
		}
	}
	t.Run("exact schema formatting", func(t *testing.T) {
		changed(t, base, append(slices.Clone(contract), '\n'), resources, capabilities, models)
	})
	for _, tc := range []struct {
		name string
		edit func(*collectionValidationProfileData)
	}{
		{"policy", func(p *collectionValidationProfileData) { p.ValidationPolicyVersion++ }},
		{"api", func(p *collectionValidationProfileData) { p.APIVersion = "cpra.io/v3" }},
		{"compiler", func(p *collectionValidationProfileData) { p.CompilerVersion += ".next" }},
		{"codec", func(p *collectionValidationProfileData) { p.PlanCodecVersion++ }},
		{"platform", func(p *collectionValidationProfileData) { p.GOOS = "other-platform" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := base
			tc.edit(&next)
			changed(t, next, contract, resources, capabilities, models)
		})
	}
	limits := reflect.TypeFor[collectionValidationLimits]()
	for i := range limits.NumField() {
		t.Run(limits.Field(i).Name, func(t *testing.T) {
			next := base
			value := reflect.ValueOf(&next.Limits).Elem().Field(i)
			value.SetUint(value.Uint() + 1)
			changed(t, next, contract, resources, capabilities, models)
		})
	}
	t.Run("availability", func(t *testing.T) {
		caps := slices.Clone(capabilities)
		caps[0].Available = !caps[0].Available
		changed(t, base, contract, resources, caps, models)
	})
	t.Run("resources", func(t *testing.T) { changed(t, base, contract, resources[1:], capabilities, models) })
	t.Run("model mapping", func(t *testing.T) {
		m := slices.Clone(models)
		m[0].model, m[1].model = m[1].model, m[0].model
		changed(t, base, contract, resources, capabilities, m)
	})
	t.Run("reason excluded", func(t *testing.T) {
		caps := slices.Clone(capabilities)
		for i := range caps {
			caps[i].Reason = "private-path-and-secret-canary"
		}
		profile, digest, err := buildCollectionValidationProfile(base, contract, resources, caps, models)
		raw, _ := encodeCollectionValidationProfile(profile)
		if err != nil || digest != original || bytes.Contains(raw, []byte("canary")) {
			t.Fatal("human diagnostics entered identity")
		}
	})
}

func TestCollectionValidationProfileAll33ModelMappings(t *testing.T) {
	// Independent contract table: neither factory reflection nor OpenAPI describes
	// which concrete model the API driver's category/type dispatcher must select.
	expected := map[string]string{
		"check/http": "PulseHTTPConfig", "check/tcp": "PulseTCPConfig", "check/udp": "PulseUDPConfig", "check/dns": "PulseDNSConfig", "check/icmp": "PulseICMPConfig", "check/grpc": "PulseGRPCConfig", "check/docker": "PulseDockerConfig", "check/tls": "PulseTLSConfig", "check/redis": "PulseRedisConfig", "check/postgres": "PulsePostgresConfig", "check/mysql": "PulseMySQLConfig", "check/mongo": "PulseMongoConfig", "check/rabbitmq": "PulseRabbitMQConfig", "check/kafka": "PulseKafkaConfig",
		"recovery/docker": "InterventionTargetDocker", "recovery/webhook": "InterventionTargetWebhook", "recovery/kubernetes": "InterventionTargetKubernetes", "recovery/aws": "InterventionTargetAWS", "recovery/systemd": "InterventionTargetSystemd",
		"notification/log": "CodeNotificationLog", "notification/email": "CodeNotificationEmail", "notification/webhook": "CodeNotificationWebhook", "notification/pagerduty": "CodeNotificationPagerDuty", "notification/slack": "CodeNotificationSlack", "notification/telegram": "CodeNotificationTelegram", "notification/discord": "CodeNotificationDiscord", "notification/opsgenie": "CodeNotificationOpsgenie", "notification/teams": "CodeNotificationTeams", "notification/mattermost": "CodeNotificationMattermost", "notification/pushover": "CodeNotificationPushover", "notification/twilio": "CodeNotificationTwilio", "notification/datadog": "CodeNotificationDatadog", "notification/victorops": "CodeNotificationVictorOps",
	}
	profile, _, err := collectionValidationProfile()
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != 33 || len(profile.Drivers) != len(expected) {
		t.Fatal("incomplete reviewed driver inventory")
	}
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(api.Schema(), &contract); err != nil {
		t.Fatal(err)
	}
	capabilities := map[string]bool{}
	for _, c := range jobs.Capabilities() {
		capabilities[c.Kind+"/"+c.Driver] = c.Available
	}
	for _, driver := range profile.Drivers {
		key := driver.Kind + "/" + driver.Driver
		t.Run(key, func(t *testing.T) {
			if expected[key] != driver.ConfigModel || capabilities[key] != driver.Available {
				t.Fatal("profile mapping or compiled availability disagrees")
			}
			model, exists := contract.Components.Schemas[driver.ConfigModel]
			if !exists || len(model.Properties) == 0 {
				t.Fatal("selected concrete model has no properties")
			}
			// Null tests field admission and inert mapping without inventing sample
			// secrets or constructing a provider. Existing full-value runtime mapping
			// tests independently exercise all 33 configurations and duration handling.
			properties := map[string]any{}
			for name := range model.Properties {
				properties[name] = nil
			}
			raw, err := json.Marshal(properties)
			if err != nil {
				t.Fatal(err)
			}
			d := api.DriverConfig{Type: driver.Driver, Config: raw}
			if err := api.ValidateDriver(driver.Kind, d); err != nil {
				t.Fatalf("concrete schema fields differ from public API dispatcher: %v", err)
			}
			decoded, err := decodeRuntimeDriver(driver.Kind, d)
			if err != nil {
				t.Fatalf("concrete schema fields differ from inert runtime: %v", err)
			}
			if reflect.TypeOf(decoded).Elem().Name() != driver.ConfigModel {
				t.Fatal("runtime model differs")
			}
			d.Config = json.RawMessage(`{"not_a_reviewed_field":null}`)
			if api.ValidateDriver(driver.Kind, d) == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
}

func TestCollectionValidationProfileRejectsInvalidBuildInputs(t *testing.T) {
	type inputs struct {
		base      collectionValidationProfileData
		contract  []byte
		resources []string
		caps      []jobs.Capability
		models    []collectionProfileRuntimeModel
	}
	tests := []struct {
		name string
		edit func(*inputs)
	}{
		{"missing schema", func(i *inputs) { i.contract = nil }},
		{"malformed schema", func(i *inputs) { i.contract = []byte(`{"canary-private-path":`) }},
		{"oversized schema", func(i *inputs) { i.contract = bytes.Repeat([]byte(" "), collectionProfileSchemaBytes+1) }},
		{"missing schema model", func(i *inputs) { i.models[0].model = "MissingConfig" }},
		{"nonconcrete schema model", func(i *inputs) { i.models[0].model = "Duration" }},
		{"missing mapping", func(i *inputs) { i.models = i.models[1:] }},
		{"duplicate mapping", func(i *inputs) { i.models[1] = i.models[0] }},
		{"unknown mapping", func(i *inputs) { i.models[0].driver = "unknown" }},
		{"invalid model", func(i *inputs) { i.models[0].model = "private/path-canary" }},
		{"duplicate capability", func(i *inputs) { i.caps[1] = i.caps[0] }},
		{"unknown capability", func(i *inputs) { i.caps[0].Driver = "unknown" }},
		{"invalid kind", func(i *inputs) { i.caps[0].Kind = "external" }},
		{"too many capabilities", func(i *inputs) {
			i.caps = make([]jobs.Capability, 65)
			i.models = make([]collectionProfileRuntimeModel, 65)
		}},
		{"empty resources", func(i *inputs) { i.resources = nil }},
		{"duplicate resources", func(i *inputs) { i.resources[1] = i.resources[0] }},
		{"unsupported schema resource", func(i *inputs) { i.resources[0] = "NotAResource" }},
		{"too many resources", func(i *inputs) { i.resources = make([]string, 17) }},
		{"prepopulated output", func(i *inputs) { i.base.SchemaDigest = strings.Repeat("0", 64) }},
		{"zero policy", func(i *inputs) { i.base.ValidationPolicyVersion = 0 }},
		{"unknown profile encoding", func(i *inputs) { i.base.ProfileVersion = 2 }},
		{"zero codec", func(i *inputs) { i.base.PlanCodecVersion = 0 }},
		{"invalid platform", func(i *inputs) { i.base.GOOS = "private/path-canary" }},
		{"zero limit", func(i *inputs) { i.base.Limits.PlanBytes = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, contract, resources, caps, models := profileInputs(t)
			in := inputs{base, contract, resources, caps, models}
			tc.edit(&in)
			profile, digest, err := buildCollectionValidationProfile(in.base, in.contract, in.resources, in.caps, in.models)
			if !errors.Is(err, ErrUnavailable) || digest != "" || !reflect.DeepEqual(profile, collectionValidationProfileData{}) {
				t.Fatal("invalid build inputs yielded profile")
			}
			if strings.Contains(err.Error(), "canary") {
				t.Fatal("input diagnostics escaped")
			}
		})
	}
}

func TestCollectionValidationProfileFactoryBoundary(t *testing.T) {
	for name, factory := range map[string]func() any{
		"nil":              nil,
		"nil value":        func() any { return nil },
		"typed nil":        func() any { return (*manifest.PulseHTTPConfig)(nil) },
		"nonpointer":       func() any { return manifest.PulseHTTPConfig{} },
		"wrong package":    func() any { return &struct{ URL string }{} },
		"configured input": func() any { return &manifest.PulseHTTPConfig{Url: "private-path-canary"} },
	} {
		t.Run(name, func(t *testing.T) {
			models, err := collectionValidationRuntimeModels(map[string]func() any{"check/http": factory})
			if !errors.Is(err, ErrUnavailable) || models != nil || strings.Contains(err.Error(), "canary") {
				t.Fatal("invalid inert factory accepted")
			}
		})
	}
	for _, factories := range []map[string]func() any{nil, {"external/http": func() any { return &manifest.PulseHTTPConfig{} }}, {"check/http/extra": func() any { return &manifest.PulseHTTPConfig{} }}} {
		if _, err := collectionValidationRuntimeModels(factories); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid factory inventory accepted")
		}
	}
}
