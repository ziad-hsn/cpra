package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

type forbiddenNetwork struct{ calls int }

func (f *forbiddenNetwork) Do(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, errors.New("review fixture prohibits network requests")
}

func clearEndpointEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_SQS", "AWS_ENDPOINT_URL_ELASTIC_LOAD_BALANCING", "AWS_ENDPOINT_URL_ELASTIC_LOAD_BALANCING_V2", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func endpointTestConfig(t *testing.T, extra ...func(*config.LoadOptions) error) (aws.Config, *forbiddenNetwork, *int) {
	t.Helper()
	transport := &forbiddenNetwork{}
	credentialCalls := new(int)
	provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		*credentialCalls++
		return aws.Credentials{AccessKeyID: "review-fixture", SecretAccessKey: "review-fixture", Source: "review-fixture"}, nil
	})
	options := []func(*config.LoadOptions) error{config.WithRegion("us-east-1"), config.WithCredentialsProvider(provider), config.WithHTTPClient(transport), config.WithSharedConfigFiles([]string{}), config.WithSharedCredentialsFiles([]string{})}
	options = append(options, extra...)
	cfg, err := config.LoadDefaultConfig(context.Background(), options...)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, transport, credentialCalls
}

func TestConfiguredModeRejectsAWSEndpointEnvironmentBeforeIO(t *testing.T) {
	for _, variable := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_SQS", "AWS_ENDPOINT_URL_ELASTIC_LOAD_BALANCING", "AWS_ENDPOINT_URL_ELASTIC_LOAD_BALANCING_V2"} {
		t.Run(variable, func(t *testing.T) {
			clearEndpointEnvironment(t)
			t.Setenv(variable, "https://substitute.invalid/secret-location")
			cfg, network, credentials := endpointTestConfig(t)
			if _, err := newAWSClients(cfg); err == nil || !strings.Contains(err.Error(), "custom AWS service endpoints") || strings.Contains(err.Error(), "secret-location") {
				t.Fatalf("override was not safely rejected: %v", err)
			}
			if network.calls != 0 || *credentials != 0 {
				t.Fatalf("override reached network=%d credential retrieval=%d", network.calls, *credentials)
			}
		})
	}
}

func TestConfiguredModeRejectsAWSSharedServiceEndpointBeforeIO(t *testing.T) {
	clearEndpointEnvironment(t)
	file := filepath.Join(t.TempDir(), "aws-config")
	if err := os.WriteFile(file, []byte("[profile example]\nregion=us-east-1\nservices=example-services\n\n[services example-services]\nsqs =\n  endpoint_url = https://substitute.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, network, credentials := endpointTestConfig(t, config.WithSharedConfigFiles([]string{file}), config.WithSharedConfigProfile("example"))
	if _, err := newAWSClients(cfg); err == nil || !strings.Contains(err.Error(), "custom AWS service endpoints") {
		t.Fatalf("shared service override was not rejected: %v", err)
	}
	if network.calls != 0 || *credentials != 0 {
		t.Fatal("shared service override made a request or retrieved credentials")
	}
}

func TestNormalAndIgnoredAWSEndpointsNeedNoPreflightIO(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "ignored"}[ignore], func(t *testing.T) {
			clearEndpointEnvironment(t)
			if ignore {
				t.Setenv("AWS_ENDPOINT_URL", "https://substitute.invalid")
				t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "true")
			}
			cfg, network, credentials := endpointTestConfig(t)
			if _, err := newAWSClients(cfg); err != nil {
				t.Fatal(err)
			}
			if network.calls != 0 || *credentials != 0 {
				t.Fatal("client construction performed network or credential retrieval")
			}
		})
	}
}

func TestQUICIdentityCannotFallThroughToOrdinaryTargetMapping(t *testing.T) {
	for _, value := range []string{`"0x0123456789abcdef"`, `null`, `""`} {
		p, monitor, registration := newProcessor(t)
		body := strings.Replace(eventBody(), `"id":"i-123"`, `"id":"i-123","quicServerId":`+value, 1)
		if err := p.process(context.Background(), body); err == nil || !strings.Contains(err.Error(), "QUIC/TCP_QUIC") {
			t.Fatalf("unsupported identity was not rejected: %v", err)
		}
		if monitor.get != 0 || monitor.patches != 0 || registration.calls != 0 {
			t.Fatal("unsupported identity caused a provider or CPRa operation")
		}
	}
}
