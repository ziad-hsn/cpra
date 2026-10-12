package jobs

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestGRPCHealthMode(t *testing.T) {
	for _, tlsEnabled := range []bool{false, true} {
		for _, status := range []healthpb.HealthCheckResponse_ServingStatus{healthpb.HealthCheckResponse_SERVING, healthpb.HealthCheckResponse_NOT_SERVING, healthpb.HealthCheckResponse_UNKNOWN} {
			t.Run(strconv.FormatBool(tlsEnabled)+"/"+status.String(), func(t *testing.T) {
				fixture := httptest.NewTLSServer(http.NotFoundHandler())
				fixture.Close()
				var options []grpc.ServerOption
				ca := ""
				if tlsEnabled {
					options = append(options, grpc.Creds(credentials.NewTLS(fixture.TLS)))
					ca = filepath.Join(t.TempDir(), "ca.pem")
					if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.TLS.Certificates[0].Certificate[0]}), 0600); err != nil {
						t.Fatal(err)
					}
				}
				server := grpc.NewServer(options...)
				healthService := health.NewServer()
				healthService.SetServingStatus("billing", status)
				healthService.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
				healthpb.RegisterHealthServer(server, healthService)
				address := serveGRPCFixture(t, server)
				job, err := CreatePulseJob(manifest.Pulse{Type: "grpc", Timeout: time.Second, Config: &manifest.PulseGRPCConfig{Host: "127.0.0.1", Port: address.Port, Mode: "health", Service: "billing", AllowInsecure: !tlsEnabled, CAFile: ca}}, ecs.Entity{})
				if err != nil {
					t.Fatal(err)
				}
				result := job.Execute()
				if (result.Err == nil) != (status == healthpb.HealthCheckResponse_SERVING) {
					t.Fatalf("incorrect health result: %v", result.Err)
				}
				missing := job.Copy().(*PulseGRPCJob)
				missing.Service = "missing-service"
				if missing.Execute().Err == nil {
					t.Fatal("missing service fell back to TCP")
				}
			})
		}
	}
}

func TestGRPCMissingHealthNeverFallsBackToTCP(t *testing.T) {
	address := serveGRPCFixture(t, grpc.NewServer())
	job := &PulseGRPCJob{Host: "127.0.0.1", Port: address.Port, Timeout: 100 * time.Millisecond}
	if job.Execute().Err != nil {
		t.Fatal("omitted mode did not preserve TCP reachability")
	}
	job.Mode, job.AllowInsecure = "health", true
	if job.Execute().Err == nil {
		t.Fatal("missing health RPC was treated as healthy")
	}
	job.AllowInsecure = false
	if job.Execute().Err == nil {
		t.Fatal("TLS default silently used cleartext")
	}
}

func serveGRPCFixture(t *testing.T, server *grpc.Server) *net.TCPAddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	return listener.Addr().(*net.TCPAddr)
}

func TestDockerStopGraceAndOperationBudget(t *testing.T) {
	for _, fixture := range []struct {
		grace, configured, want time.Duration
		invalid                 bool
	}{
		{want: 60 * time.Second}, {grace: 50 * time.Second, want: 70 * time.Second},
		{grace: 10 * time.Second, configured: 30 * time.Second, want: 30 * time.Second},
		{grace: 10 * time.Second, configured: 29 * time.Second, invalid: true},
		{grace: -time.Second, invalid: true}, {configured: -time.Second, invalid: true},
	} {
		budget, err := dockerOperationBudget(fixture.grace, fixture.configured)
		if (err != nil) != fixture.invalid || !fixture.invalid && budget != fixture.want {
			t.Fatalf("incorrect budget: %v, %v", budget, err)
		}
	}
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("t") != "1" {
			t.Error("Docker did not receive rounded stop grace")
		}
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	t.Setenv("DOCKER_HOST", target.URL)
	t.Setenv("DOCKER_API_VERSION", "1.52")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	job, err := CreateInterventionJob(manifest.Intervention{Action: "docker", Target: &manifest.InterventionTargetDocker{Container: "fixture", Timeout: 10 * time.Millisecond}}, ecs.Entity{})
	if err != nil {
		t.Fatal(err)
	}
	if result := job.Execute(); result.Err != nil {
		t.Fatalf("stop grace bounded the whole operation: %v", result.Err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.(*InterventionDockerJob).SetContext(ctx)
	cancel()
	if result := job.Execute(); result.Err == nil || errors.Is(result.Err, ErrInterventionRejected) {
		t.Fatal("interruption was confirmed rejection")
	}
	if calls.Load() != 1 {
		t.Fatal("interrupted operation was replayed")
	}
}

func TestICMPPrivilegeModes(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows", "freebsd"} {
		for _, unprivileged := range []bool{false, true} {
			privileged, err := icmpPrivileged(platform, unprivileged)
			unsupported := unprivileged && platform != "linux" && platform != "darwin"
			if (err != nil) != unsupported || !unsupported && privileged == unprivileged {
				t.Fatalf("incorrect ICMP mode for %s: %v %v", platform, privileged, err)
			}
		}
	}
}
