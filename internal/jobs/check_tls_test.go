package jobs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func checkTestCertificate(t *testing.T, expired bool) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	end := now.Add(7 * 24 * time.Hour)
	if expired {
		end = now.Add(-time.Hour)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-48 * time.Hour), NotAfter: end, DNSNames: []string{"db.example"}, BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, file
}

func TestCheckTLSHTTPPolicyIsolation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		certificate, ca := checkTestCertificate(t, expired)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
		server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
		server.Config.ErrorLog = log.New(io.Discard, "", 0)
		server.StartTLS()
		t.Cleanup(server.Close)
		for _, test := range []struct {
			name, ca, server string
			skip, want       bool
		}{
			{"trusted", ca, "db.example", false, !expired},
			{"self-signed", "", "db.example", false, false},
			{"wrong-host", ca, "wrong.example", false, false},
			{"explicit-skip", "", "wrong.example", true, true},
			{"verified-after-skip", "", "db.example", false, false},
		} {
			t.Run(test.name+"/expired="+map[bool]string{true: "true", false: "false"}[expired], func(t *testing.T) {
				job, err := CreatePulseJob(manifest.Pulse{Type: "http", Timeout: time.Second, Config: &manifest.PulseHTTPConfig{Url: server.URL, CAFile: test.ca, ServerName: test.server, InsecureSkipVerify: test.skip}}, ecs.Entity{})
				if err != nil {
					t.Fatal(err)
				}
				if result := job.Execute(); (result.Err == nil) != test.want {
					t.Fatal("wrong TLS result", result.Err)
				}
			})
		}
	}
}

func TestCheckTLSGRPCPolicyAndExpiry(t *testing.T) {
	certificate, ca := checkTestCertificate(t, false)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}})))
	healthService := health.NewServer()
	healthService.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthService)
	address := serveGRPCFixture(t, server)
	for _, test := range []struct {
		ca, name   string
		skip, want bool
	}{{ca, "db.example", false, true}, {ca, "wrong.example", false, false}, {"", "wrong.example", true, true}, {"", "db.example", false, false}} {
		job := &PulseGRPCJob{Mode: "health", Host: "127.0.0.1", Port: address.Port, Timeout: 200 * time.Millisecond, CAFile: test.ca, ServerName: test.name, InsecureSkipVerify: test.skip}
		if result := job.Execute(); (result.Err == nil) != test.want {
			t.Fatal("wrong gRPC TLS result", result.Err)
		}
	}
	expired, _ := checkTestCertificate(t, true)
	fixture := httptest.NewUnstartedServer(http.NotFoundHandler())
	fixture.TLS = &tls.Config{Certificates: []tls.Certificate{expired}}
	fixture.Config.ErrorLog = log.New(io.Discard, "", 0)
	fixture.StartTLS()
	defer fixture.Close()
	addr := fixture.Listener.Addr().(*net.TCPAddr)
	job, err := newPulseTLSJob(&manifest.PulseTLSConfig{Host: "127.0.0.1", Port: addr.Port, InsecureSkipVerify: true, CriticalDays: 1}, time.Second, ecs.Entity{})
	if err != nil {
		t.Fatal(err)
	}
	result := job.Execute()
	if result.Err == nil || result.Payload["not_after"] == nil {
		t.Fatal("skip verification disabled configured expiry check", result.Err)
	}
}

func TestCheckTLSBoundedSafeCAErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private-marker.pem")
	for _, contents := range []string{"private-body-marker", strings.Repeat("x", (1<<20)+1)} {
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := checkTLSConfiguration(file, "db.example", false)
		if err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatal("unsafe CA error", err)
		}
	}
}
