package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
)

func (p *PulseGRPCJob) healthCheck(ctx context.Context) (result Result) {
	result = Result{ID: p.ID, Ent: p.Entity, Payload: p.payload}
	transport, err := p.healthCredentials()
	if err != nil {
		result.Err = err
		return
	}
	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	connection, err := grpc.NewClient("passthrough:///"+address,
		grpc.WithTransportCredentials(transport), grpc.WithNoProxy(),
		grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", address)
		}),
	)
	if err != nil {
		result.Err = fmt.Errorf("gRPC health client initialization failed")
		return
	}
	defer func() { _ = connection.Close() }()
	attempts := newPulseAttempts(ctx, p.Retries)
	defer attempts.complete(&result)
	client := health.NewHealthClient(connection)
	for {
		attempt, ok := attempts.next()
		if !ok {
			return
		}
		response, err := client.Check(attempt, &health.HealthCheckRequest{Service: p.Service})
		if err == nil && response.GetStatus() == health.HealthCheckResponse_SERVING {
			result.Err = nil
			return
		}
		if err != nil {
			// Remote RPC messages are untrusted diagnostics, not retained state.
			result.Err = fmt.Errorf("gRPC health RPC failed")
		} else {
			result.Err = fmt.Errorf("gRPC health service is not serving")
		}
	}
}

func (p *PulseGRPCJob) healthCredentials() (credentials.TransportCredentials, error) {
	if p.AllowInsecure {
		if p.CAFile != "" || p.ServerName != "" {
			return nil, fmt.Errorf("gRPC cleartext cannot also configure TLS trust or server name")
		}
		return insecure.NewCredentials(), nil
	}
	configuration := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.ServerName}
	if p.CAFile != "" {
		file, err := os.Open(p.CAFile)
		if err != nil {
			return nil, fmt.Errorf("gRPC CA file is unavailable")
		}
		defer func() { _ = file.Close() }()
		pem, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(pem) > 1<<20 {
			return nil, fmt.Errorf("gRPC CA file cannot be read within its 1 MiB limit")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("gRPC CA file contains no supported certificates")
		}
		configuration.RootCAs = roots
	}
	return credentials.NewTLS(configuration), nil
}
