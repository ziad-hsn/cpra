// This example demonstrates programmatically creating a monitor manifest
// instead of editing YAML by hand, mirroring the "How to Create a Custom Monitor" guide.
//
// Usage: go run example-create-monitor.go
// Expected output: path to the generated YAML file and a sample of its contents.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cpra/internal/loader/schema"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "create-monitor example failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	manifest := schema.Manifest{
		Monitors: []schema.Monitor{
			{
				Name:    "orders-api-health",
				Enabled: true,
				Pulse: schema.Pulse{
					Type:     "http",
					Interval: 30 * time.Second,
					Timeout:  5 * time.Second,
					Config: &schema.PulseHTTPConfig{
						Url:     "https://orders.internal/api/health",
						Method:  "GET",
						Headers: schema.StringList{"Accept: application/json"},
						Retries: 2,
					},
					HealthyThreshold:   2,
					UnhealthyThreshold: 3,
				},
				Intervention: schema.Intervention{
					Action:  "docker",
					Retries: 1,
					Target: &schema.InterventionTargetDocker{
						Type:      "docker",
						Container: "orders-api",
						Timeout:   45 * time.Second,
					},
				},
				Codes: schema.Codes{
					"red": {
						Dispatch: true,
						Notify:   "pagerduty",
						Config: &schema.CodeNotificationPagerDuty{
							URL: "https://events.pagerduty.com/v2/enqueue",
						},
					},
					"yellow": {
						Dispatch: true,
						Notify:   "log",
						Config: &schema.CodeNotificationLog{
							File: "/var/log/cpra/orders-warnings.log",
						},
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	_ = encoder.Close()

	outputPath := filepath.Join(os.TempDir(), "cpra-monitor-example.yaml")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Printf("Monitor manifest written to %s\n", outputPath)
	fmt.Println("Preview:\n" + buf.String())

	return nil
}
