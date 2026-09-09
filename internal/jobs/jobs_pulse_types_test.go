package jobs

import (
	"testing"
	"time"

	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark/ecs"
)

func TestCreatePulseJobNewTypes(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	cases := []struct {
		name  string
		pulse schema.Pulse
	}{
		{"dns", schema.Pulse{Type: "dns", Timeout: time.Second, Config: &schema.PulseDNSConfig{Host: "example.com"}}},
		{"udp", schema.Pulse{Type: "udp", Timeout: time.Second, Config: &schema.PulseUDPConfig{Host: "127.0.0.1", Port: 53}}},
		{"grpc", schema.Pulse{Type: "grpc", Timeout: time.Second, Config: &schema.PulseGRPCConfig{Host: "127.0.0.1", Port: 50051}}},
		{"docker", schema.Pulse{Type: "docker", Timeout: time.Second, Config: &schema.PulseDockerConfig{Container: "foo"}}},
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
