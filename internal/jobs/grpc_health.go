package jobs

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
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
			result.Err = &GRPCHealthError{RPCCode: status.Code(err)}
		} else {
			state := response.GetStatus().String()
			if state != "NOT_SERVING" && state != "SERVICE_UNKNOWN" {
				state = "UNKNOWN"
			}
			result.Err = &GRPCHealthError{Status: state}
		}
	}
}

func (p *PulseGRPCJob) healthCredentials() (credentials.TransportCredentials, error) {
	if p.AllowInsecure {
		if p.CAFile != "" || p.ServerName != "" || p.InsecureSkipVerify {
			return nil, fmt.Errorf("gRPC cleartext cannot also configure TLS trust or server name")
		}
		return insecure.NewCredentials(), nil
	}
	configuration, err := checkTLSConfiguration(p.CAFile, p.ServerName, p.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(configuration), nil
}
