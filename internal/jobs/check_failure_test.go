package jobs

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"google.golang.org/grpc/codes"
)

func TestClassifyCheckFailureTypedEvidence(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	certificate := &x509.Certificate{NotBefore: at.Add(-time.Hour), NotAfter: at.Add(-time.Minute)}
	for _, tc := range []struct {
		name, driver, code, phase string
		err                       error
		eligible                  bool
	}{
		{"refused", "tcp", "network.connection_refused", "connect", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, true},
		{"refused text is not proof", "tcp", "operation_failed", "check", errors.New("dial: connection refused password=secret-marker"), false},
		{"local descriptors", "tcp", "check.unavailable", "local", &net.OpError{Op: "dial", Err: syscall.EMFILE}, false},
		{"local source address", "tcp", "check.unavailable", "local", &net.OpError{Op: "dial", Err: syscall.EADDRNOTAVAIL}, false},
		{"local occupied address", "tcp", "check.unavailable", "local", &net.OpError{Op: "dial", Err: syscall.EADDRINUSE}, false},
		{"local capacity wait", "icmp", "check.unavailable", "local", &checkUnavailableError{cause: context.DeadlineExceeded}, false},
		{"cancelled", "http", "cancelled", "local", context.Canceled, false},
		{"deadline", "tcp", "deadline_exceeded", "check", context.DeadlineExceeded, true},
		{"CA missing", "http", "ca_file_missing", "local", &ConfigurationError{Code: "ca_file_missing"}, false},
		{"DNS not found", "dns", "dns.not_found", "resolve", &net.DNSError{IsNotFound: true, Name: "private-secret"}, true},
		{"DNS temporary unspecified", "dns", "dns_failed", "resolve", &net.DNSError{IsTemporary: true, Err: "SERVFAIL secret-marker"}, true},
		{"DNS timeout", "dns", "dns.timeout", "resolve", &net.DNSError{IsTimeout: true}, true},
		{"wrong host", "tls", "tls.cert.hostname", "tls", x509.HostnameError{Host: "private-secret"}, true},
		{"untrusted", "http", "tls.cert.untrusted", "tls", x509.UnknownAuthorityError{}, true},
		{"expired", "tls", "tls.cert.expired", "tls", x509.CertificateInvalidError{Reason: x509.Expired, Cert: certificate}, true},
		{"missing expiry evidence", "tls", "tls.cert.invalid", "tls", x509.CertificateInvalidError{Reason: x509.Expired}, true},
		{"container stopped", "docker", "docker.not_running", "response", &checkConditionError{code: "docker.not_running"}, true},
		{"daemon refused", "docker", "check.unavailable", "local", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, false},
		{"ICMP no reply", "icmp", "icmp.no_reply", "response", &checkConditionError{code: "icmp.no_reply"}, true},
		{"UDP empty", "udp", "udp.empty_response", "response", &checkConditionError{code: "udp.empty_response"}, true},
		{"HTTP status", "http", "unexpected_http_status", "response", &HTTPStatusError{Status: 503}, true},
		{"RPC transport", "grpc", "grpc.rpc_failed", "check", &GRPCHealthError{RPCCode: codes.Unavailable}, true},
		{"RPC cancelled", "grpc", "cancelled", "local", &GRPCHealthError{RPCCode: codes.Canceled}, false},
		{"RPC health", "grpc", "grpc_health_failed", "response", &GRPCHealthError{Status: "NOT_SERVING"}, true},
		{"invalid remote enum", "grpc", "operation_failed", "check", &GRPCHealthError{Status: "secret-marker"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SanitizeError(fmt.Errorf("wrapper private-secret: %w", tc.err))
			observation := ClassifyCheckFailure(tc.driver, err, at)
			if observation.Code != tc.code || observation.Phase != tc.phase {
				t.Fatalf("got %+v", observation)
			}
			if err := api.ValidateFailureObservation(tc.driver, observation); err != nil {
				t.Fatal(err)
			}
			definition, _ := api.FailureCodeDefinition(observation.TaxonomyVersion, observation.Code)
			if definition.Eligible != tc.eligible {
				t.Fatalf("eligibility: %+v", definition)
			}
			if strings.Contains(observation.Message, "private-secret") || strings.Contains(observation.Message, "secret-marker") {
				t.Fatal("remote detail leaked")
			}
		})
	}
}

func TestClassifyCheckFailureAllDriverMappings(t *testing.T) {
	for _, driver := range []string{"http", "tcp", "udp", "dns", "icmp", "grpc", "docker", "tls", "redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka"} {
		t.Run(driver, func(t *testing.T) {
			observation := ClassifyCheckFailure(driver, &ConfigurationError{Code: "ca_file_invalid"}, time.Now())
			if err := api.ValidateFailureObservation(driver, observation); err != nil {
				t.Fatal(err)
			}
			if observation.Code != "ca_file_invalid" {
				t.Fatalf("local failure became target failure: %+v", observation)
			}
			observation = ClassifyCheckFailure(driver, errors.New("unclassified remote text"), time.Now())
			definition, _ := api.FailureCodeDefinition(observation.TaxonomyVersion, observation.Code)
			if definition.Eligible {
				t.Fatalf("generic failure became actionable: %+v", observation)
			}
		})
	}
}

func TestClassifyCheckFailureHTTPProductionPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("secret-marker"))
	}))
	defer server.Close()
	job := &PulseHTTPJob{URL: server.URL, Timeout: time.Second, ExpectedStatus: []int{200}}
	result := job.Execute()
	observation := ClassifyCheckFailure("http", result.Err, time.Now())
	if observation.Code != "unexpected_http_status" || observation.HTTPStatus == nil || *observation.HTTPStatus != 503 {
		t.Fatalf("status: %+v (%v)", observation, result.Err)
	}
	if effect := ClassifyEffect(result.Err); effect.Outcome != EffectUnknown {
		t.Fatalf("check classification granted replay: %v", effect)
	}
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer secure.Close()
	job.URL = secure.URL
	result = job.Execute()
	observation = ClassifyCheckFailure("http", result.Err, time.Now())
	if observation.Code != "tls.cert.untrusted" {
		t.Fatalf("TLS production path: %+v", observation)
	}
	address := server.Listener.Addr().String()
	server.Close()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if connection != nil {
		_ = connection.Close()
		t.Fatal("closed listener accepted a connection")
	}
	observation = ClassifyCheckFailure("tcp", err, time.Now())
	if observation.Code != "network.connection_refused" {
		t.Fatalf("refusal: %+v (%v)", observation, err)
	}
}
