package jobs

import (
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"

	"github.com/mlange-42/ark/ecs"
)

func TestCreatePulseJobNewTypes(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	cases := []struct {
		name  string
		pulse manifest.Pulse
	}{
		{"dns", manifest.Pulse{Type: "dns", Timeout: time.Second, Config: &manifest.PulseDNSConfig{Host: "example.com"}}},
		{"udp", manifest.Pulse{Type: "udp", Timeout: time.Second, Config: &manifest.PulseUDPConfig{Host: "127.0.0.1", Port: 53}}},
		{"grpc", manifest.Pulse{Type: "grpc", Timeout: time.Second, Config: &manifest.PulseGRPCConfig{Host: "127.0.0.1", Port: 50051}}},
		{"docker", manifest.Pulse{Type: "docker", Timeout: time.Second, Config: &manifest.PulseDockerConfig{Container: "foo"}}},
	}

	for _, tc := range cases {
		job, err := CreatePulseJob(tc.pulse, ent)
		if err != nil {
			t.Fatalf("%s: CreatePulseJob: %v", tc.name, err)
		}
		if job == nil || job.IsNil() {
			t.Fatalf("%s: nil job", tc.name)
		}
	}
}
