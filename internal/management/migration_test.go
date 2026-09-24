package management

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func TestBootstrapExtractionPreservesManifestExecutionFingerprint(t *testing.T) {
	const source = `{"monitors":[{"id":"service","name":"Billing API","enabled":true,"pulse_check":{"type":"http","interval":"60s","timeout":"5s","config":{"url":"https://example.test/health?token=private-query","headers":{"Authorization":"Bearer private-header"},"body":"{\"token\":\"private-body\"}"}},"codes":{"red":{"notify":"slack","config":{"hook":"https://example.test/private-webhook"}}}}]}`
	var old manifest.Manifest
	if err := json.Unmarshal([]byte(source), &old); err != nil {
		t.Fatal(err)
	}
	expected, err := manifest.ConfigurationRevision(old.Monitors[0], old.Endpoints, old.NotificationGroups)
	if err != nil {
		t.Fatal(err)
	}
	var input api.Resource
	if err := collection.Decode(context.Background(), strings.NewReader(source), collection.DecodeOptions{}, func(item collection.Item) error { input = item.Resource; return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(input)
	converted, credentials, err := ExtractInlineCredentials(input)
	if err != nil || len(credentials) != 4 {
		t.Fatal("bootstrap fields not extracted", err, len(credentials))
	}
	after, _ := json.Marshal(input)
	if !bytes.Equal(before, after) {
		t.Fatal("converter changed caller-owned input")
	}
	raw, _ := json.Marshal(converted)
	for _, private := range []string{"private-query", "private-header", "private-body", "private-webhook"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("inline secret stayed in editable representation")
		}
	}
	second, again, err := ExtractInlineCredentials(input)
	if err != nil || !reflect.DeepEqual(converted, second) || !reflect.DeepEqual(credentials, again) {
		t.Fatal("repeated migration changed private identities", err)
	}
	c, _ := testCatalog(t)
	for _, credential := range credentials {
		createResource(t, c, credential)
	}
	createResource(t, c, converted)
	view, _ := c.Snapshot()
	runtime, err := c.PrepareRuntime(context.Background(), view, "service")
	if err != nil || runtime.ExecutionRevision != expected {
		t.Fatal("representation migration invalidated old execution identity", err)
	}
	var driver api.MonitorSpec
	if err := json.Unmarshal(converted.Spec, &driver); err != nil {
		t.Fatal(err)
	}
	if driver.Check.Driver.CredentialRefs == nil || len(*driver.Check.Driver.CredentialRefs) != 3 {
		t.Fatal("missing protected check fields")
	}
}

func TestBootstrapPrivateCredentialIdentityIsOwnerAndPurposeScoped(t *testing.T) {
	makeEndpoint := func(id, hook string) api.Resource {
		config, _ := json.Marshal(map[string]string{"hook": hook})
		return resource("NotificationEndpoint", id, api.DriverConfig{Type: "slack", Config: config})
	}
	_, one, err := ExtractInlineCredentials(makeEndpoint("shared", "https://example.test/first"))
	if err != nil {
		t.Fatal(err)
	}
	_, rotated, err := ExtractInlineCredentials(makeEndpoint("shared", "https://example.test/second"))
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ExtractInlineCredentials(makeEndpoint("other", "https://example.test/first"))
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Metadata.ID != rotated[0].Metadata.ID || one[0].Metadata.ID == other[0].Metadata.ID {
		t.Fatal("private identity depends on secret or aliases owners")
	}
	if bytes.Equal(one[0].Spec, rotated[0].Spec) {
		t.Fatal("rotation lost supplied value")
	}
	conflicting := resource("NotificationEndpoint", "shared", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{"hook":"https://example.test/private"}`), CredentialRefs: api.Pointer(map[string]string{"hook": "explicit"})})
	if _, _, err := ExtractInlineCredentials(conflicting); err == nil || strings.Contains(err.Error(), "example.test/private") {
		t.Fatal("conflicting inline secret was not rejected safely", err)
	}
}

func TestBootstrapManifestZeroInheritanceMatchesPriorExecution(t *testing.T) {
	const source = `{"monitors":[{"id":"inherited","name":"prior defaults","pulse_check":{"type":"http","interval":"60s","timeout":"5s","retries":3,"max_failures":4,"unhealthy_threshold":0,"config":{"url":"https://example.test/health","retries":0}}}]}`
	var configuration manifest.Manifest
	if err := json.Unmarshal([]byte(source), &configuration); err != nil {
		t.Fatal(err)
	}
	expected, err := manifest.ConfigurationRevision(configuration.Monitors[0], configuration.Endpoints, configuration.NotificationGroups)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testCatalog(t)
	if err := collection.Decode(context.Background(), strings.NewReader(source), collection.DecodeOptions{}, func(item collection.Item) error {
		converted, credentials, err := ExtractInlineCredentials(item.Resource)
		if err != nil {
			return err
		}
		for _, credential := range credentials {
			createResource(t, c, credential)
		}
		createResource(t, c, converted)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, _ := c.Snapshot()
	got, err := c.PrepareRuntime(context.Background(), view, "inherited")
	if err != nil || got.ExecutionRevision != expected {
		t.Fatal("manifest zero inheritance changed execution fingerprint", err)
	}
	if got.Monitor.Pulse.Config.(*manifest.PulseHTTPConfig).Retries != 3 || got.Monitor.Pulse.UnhealthyThreshold != 4 {
		t.Fatal("old default inheritance was lost")
	}
}
