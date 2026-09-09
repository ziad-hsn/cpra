package schema

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPulseNewTypesDecode(t *testing.T) {
	data := []byte(`
monitors:
  - name: dns-check
    pulse_check:
      type: dns
      interval: 1m
      timeout: 5s
      config:
        host: example.com
  - name: udp-check
    pulse_check:
      type: udp
      interval: 1m
      timeout: 5s
      config:
        host: 127.0.0.1
        port: 53
  - name: grpc-check
    pulse_check:
      type: grpc
      interval: 1m
      timeout: 5s
      config:
        host: 127.0.0.1
        port: 50051
  - name: docker-check
    pulse_check:
      type: docker
      interval: 1m
      timeout: 5s
      config:
        container: my-container
`)

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Monitors) != 4 {
		t.Fatalf("expected 4 monitors, got %d", len(m.Monitors))
	}

	expected := []struct {
		name string
		typ  string
	}{
		{"dns-check", "dns"},
		{"udp-check", "udp"},
		{"grpc-check", "grpc"},
		{"docker-check", "docker"},
	}
	for i, e := range expected {
		mon := m.Monitors[i]
		if mon.Pulse.Type != e.typ {
			t.Fatalf("monitor %d: expected type %q, got %q", i, e.typ, mon.Pulse.Type)
		}
		if mon.Pulse.Config == nil {
			t.Fatalf("monitor %d (%s): nil config", i, e.name)
		}
	}

	if _, ok := m.Monitors[0].Pulse.Config.(*PulseDNSConfig); !ok {
		t.Fatalf("expected DNS config, got %T", m.Monitors[0].Pulse.Config)
	}
	if _, ok := m.Monitors[1].Pulse.Config.(*PulseUDPConfig); !ok {
		t.Fatalf("expected UDP config, got %T", m.Monitors[1].Pulse.Config)
	}
	if _, ok := m.Monitors[2].Pulse.Config.(*PulseGRPCConfig); !ok {
		t.Fatalf("expected gRPC config, got %T", m.Monitors[2].Pulse.Config)
	}
	if _, ok := m.Monitors[3].Pulse.Config.(*PulseDockerConfig); !ok {
		t.Fatalf("expected docker config, got %T", m.Monitors[3].Pulse.Config)
	}
}
