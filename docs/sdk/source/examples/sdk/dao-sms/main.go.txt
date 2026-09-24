//go:build externaljobs

// dao-sms runs a read-only governance RPC check and an internal SMS handler in
// an external CPRa worker. Its default demo contacts loopback fixtures only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ziad-hsn/cpra/examples/sdk/internal/clientconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/worker"
)

type localConfig struct {
	RPC struct {
		URL       string `json:"url"`
		TokenFile string `json:"tokenFile"`
	} `json:"rpc"`
	SMS struct {
		URL        string            `json:"url"`
		TokenFile  string            `json:"tokenFile"`
		Recipients map[string]string `json:"recipients"`
	} `json:"sms"`
}

func readToken(path string, required bool) (string, error) {
	if path == "" && !required {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open worker-local token file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("worker-local token file must be a regular file")
	}
	raw, err := readBounded(file, 16384)
	if err != nil {
		return "", errors.New("worker-local token file is unreadable or exceeds 16 KiB")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("worker-local token is empty or contains invalid characters")
	}
	return token, nil
}

func localCredentials(path string, allowLoopbackHTTP bool) (worker.CredentialResolver, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open worker-local configuration")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("worker-local configuration must be a regular file")
	}
	raw, err := readBounded(file, 65536)
	if err != nil {
		return nil, errors.New("worker-local configuration is unreadable or exceeds 64 KiB")
	}
	var config localConfig
	if err = api.StrictDecode(raw, &config); err != nil {
		return nil, errors.New("worker-local configuration is invalid")
	}
	for _, address := range []string{config.RPC.URL, config.SMS.URL} {
		if err = validateProviderURL(address, allowLoopbackHTTP); err != nil {
			return nil, err
		}
	}
	rpcToken, err := readToken(config.RPC.TokenFile, false)
	if err != nil {
		return nil, err
	}
	smsToken, err := readToken(config.SMS.TokenFile, true)
	if err != nil {
		return nil, err
	}
	if config.SMS.Recipients["dao-oncall"] == "" {
		return nil, errors.New("worker-local recipient alias dao-oncall is required")
	}
	return func(ctx context.Context, profile string) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch profile {
		case rpcProfile:
			return rpcCredentials{URL: config.RPC.URL, Token: rpcToken}, nil
		case smsProfile:
			recipients := make(map[string]string, len(config.SMS.Recipients))
			for alias, recipient := range config.SMS.Recipients {
				recipients[alias] = recipient
			}
			return smsCredentials{URL: config.SMS.URL, Token: smsToken, Recipients: recipients}, nil
		default:
			return nil, errors.New("worker-local credential profile is unknown")
		}
	}, nil
}

func registry(client *http.Client) (*worker.Registry, error) {
	registry := worker.NewRegistry()
	if err := registry.Register(rpcJobType, jobVersion, "check", daoHealth(client, time.Now)); err != nil {
		return nil, err
	}
	if err := registry.Register(smsJobType, jobVersion, "notification", internalSMS(client)); err != nil {
		return nil, err
	}
	return registry, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dao-sms:", err)
		os.Exit(1)
	}
}

func run() error {
	mode := flag.String("mode", "demo", "demo, register, or worker")
	server := flag.String("server", "", "CPRa HTTPS origin")
	tokenFile := flag.String("token-file", "", "CPRa operator token for register, worker token for worker")
	allowHTTP := flag.Bool("allow-http", false, "explicitly permit HTTP to configured CPRa origin")
	chain := flag.String("chain-id", "0x1", "expected canonical hexadecimal chain ID")
	governor := flag.String("governor", "", "designated DAO governor contract address (required for register)")
	maxAge := flag.Int64("max-block-age", 120, "maximum observed latest block age in seconds")
	local := flag.String("local-config", "", "worker-local RPC/SMS config JSON")
	allowLocalHTTP := flag.Bool("allow-loopback-provider-http", false, "permit provider HTTP only to literal loopback IPs")
	state := flag.String("state-dir", "", "absolute private worker journal directory")
	key := flag.String("key-file", "", "absolute private raw 32-byte wrapping key outside journal directory")
	workerID := flag.String("worker-id", "dao-worker", "stable local worker identity")
	workerUID := flag.String("worker-uid", "", "immutable worker UID from local worker-auth provisioning")
	serverID := flag.String("server-id", "", "verified CPRa store/restore identity")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *mode == "demo" {
		return demo(ctx, os.Stdout)
	}
	if *mode != "register" && *mode != "worker" {
		return errors.New("mode must be demo, register, or worker")
	}
	config, err := clientconfig.FromTokenFile(*server, *tokenFile, *allowHTTP)
	if err != nil {
		return err
	}
	if *mode == "register" {
		client, err := cpra.New(config)
		if err != nil {
			return err
		}
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		result, err := register(ctx, client, rpcParameters{ExpectedChainID: *chain, Governor: *governor, MaxBlockAge: *maxAge})
		if result.OperationID != "" {
			_ = json.NewEncoder(os.Stdout).Encode(result)
		}
		return err
	}
	credentials, err := localCredentials(*local, *allowLocalHTTP)
	if err != nil {
		return err
	}
	client, err := cpra.NewWorkerClient(config)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	handlers := providerClient()
	defer handlers.CloseIdleConnections()
	registered, err := registry(handlers)
	if err != nil {
		return err
	}
	runner, err := worker.New(worker.Config{Client: client, Registry: registered, WorkerID: *workerID, WorkerUID: *workerUID, ServerID: *serverID, StateDir: *state, WrappingKeyPath: *key, Credentials: credentials, Limits: worker.Limits{Concurrency: 2}, DrainTimeout: 45 * time.Second})
	if err != nil {
		return err
	}
	defer runner.Close()
	err = runner.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
