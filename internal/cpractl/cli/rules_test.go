package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesTestOfflineAndAssertion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	input := `apiVersion: cpra.io/v2
kind: Monitor
metadata:
  id: service
spec:
  enabled: true
  check:
    interval: 60s
    timeout: 5s
    driver:
      type: http
      config:
        url: https://service.invalid/health
  recovery:
    taxonomyVersion: "1"
    rules:
      - id: network
        match:
          codes: [network.connection_refused]
        none: true
`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "/absent/token")
	t.Setenv("CPRA_SERVER", "https://must-not-connect.invalid")
	for _, expect := range []string{"network", "wrong"} {
		root := NewRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"rules", "test", "-f", path, "--monitor-id", "service", "--code", "network.connection_refused", "--phase", "connect", "--expect-rule", expect, "-o", "json"})
		err := root.ExecuteContext(t.Context())
		if (err == nil) != (expect == "network") || !strings.Contains(out.String(), `"ruleID": "network"`) {
			t.Fatal(out.String(), err)
		}
	}
}

func TestRulesExamplesOffline(t *testing.T) {
	path := filepath.Join("..", "..", "..", "examples", "intervention-rules", "monitor.yaml")
	for _, tc := range []struct{ code, phase, status, want string }{
		{"network.connection_refused", "connect", "", "restart-refused-container"},
		{"unexpected_http_status", "response", "401", "leave-authentication-to-operator"},
		{"unexpected_http_status", "response", "503", "agent-on-service-unavailable"},
		{"unexpected_http_status", "response", "404", "@none"},
	} {
		root := NewRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		args := []string{"rules", "test", "-f", path, "--monitor-id", "checkout", "--code", tc.code, "--phase", tc.phase, "--expect-rule", tc.want}
		if tc.status != "" {
			args = append(args, "--http-status", tc.status)
		}
		root.SetArgs(args)
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatal(out.String(), err)
		}
	}
}
