//go:build externaljobs

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/worker"
)

const (
	rpcJobType = "dao-rpc-health"
	smsJobType = "internal-sms"
	jobVersion = "1"
	rpcProfile = "governance-rpc"
	smsProfile = "governance-sms"
	monitorID  = "dao-governance-rpc"
)

// URLs and secrets come from the worker's local configuration. Assignments may
// select a named profile but cannot make this worker contact an arbitrary URL.
type rpcCredentials struct{ URL, Token string }
type smsCredentials struct {
	URL, Token string
	Recipients map[string]string
}

type rpcParameters struct {
	ExpectedChainID string `json:"expectedChainID"`
	Governor        string `json:"governor"`
	MaxBlockAge     int64  `json:"maxBlockAgeSeconds"`
}

type smsParameters struct {
	RecipientAlias string `json:"recipientAlias"`
	Text           string `json:"text"`
}

func (p rpcParameters) validate() error {
	if _, err := quantity(p.ExpectedChainID); err != nil {
		return errors.New("expectedChainID must be a canonical hexadecimal quantity")
	}
	address, err := hexBytes(p.Governor)
	if err != nil || len(address) != 20 {
		return errors.New("governor must be a 20-byte hexadecimal contract address")
	}
	if p.MaxBlockAge < 1 || p.MaxBlockAge > 3600 {
		return errors.New("maxBlockAgeSeconds must be between 1 and 3600")
	}
	return nil
}

func (p smsParameters) validate() error {
	if p.RecipientAlias == "" || len(p.RecipientAlias) > 64 || strings.ContainsAny(p.RecipientAlias, "\r\n") {
		return errors.New("recipientAlias must be a nonempty local alias of at most 64 bytes")
	}
	if !utf8.ValidString(p.Text) || utf8.RuneCountInString(p.Text) < 1 || utf8.RuneCountInString(p.Text) > 480 {
		return errors.New("text must contain between 1 and 480 Unicode characters")
	}
	return nil
}

func quantity(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") || len(value) < 3 || (len(value) > 3 && value[2] == '0') {
		return 0, errors.New("invalid hexadecimal quantity")
	}
	return strconv.ParseUint(value[2:], 16, 64)
}

func hexBytes(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "0x") {
		return nil, errors.New("missing hexadecimal prefix")
	}
	return hex.DecodeString(value[2:])
}

// Read at most limit+1 so oversized JSON fails explicitly instead of decoding a
// truncated prefix. Provider response bodies never enter diagnostics or logs.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("response exceeds limit")
	}
	return b, nil
}

func providerClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func validateProviderURL(address string, allowLoopbackHTTP bool) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("provider URL must have an origin, no user information, query credentials, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if allowLoopbackHTTP && u.Scheme == "http" && ip != nil && ip.IsLoopback() {
		return nil
	}
	return errors.New("provider HTTPS is required; explicit HTTP permission accepts only a literal loopback address")
}

func rpcCall(ctx context.Context, client *http.Client, credentials rpcCredentials, method string, params []any, result any) error {
	body, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{"2.0", 1, method, params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if credentials.Token != "" {
		req.Header.Set("Authorization", "Bearer "+credentials.Token)
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("RPC did not return HTTP 200")
	}
	raw, err := readBounded(response.Body, 1<<20)
	if err != nil {
		return err
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err = api.StrictDecode(raw, &envelope); err != nil {
		return errors.New("invalid RPC response")
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != 1 || len(envelope.Error) != 0 || len(envelope.Result) == 0 || bytes.Equal(envelope.Result, []byte("null")) {
		return errors.New("RPC error or missing result")
	}
	return json.Unmarshal(envelope.Result, result)
}

func daoHealth(client *http.Client, now func() time.Time) worker.Handler {
	return func(parent context.Context, job worker.Job) (api.Outcome, error) {
		ctx, cancel := context.WithTimeout(parent, 4*time.Second)
		defer cancel()
		credentials, ok := job.Credentials.(rpcCredentials)
		if !ok || credentials.URL == "" {
			return api.Outcome{Status: "noData", Diagnostic: "RPC credential profile is unavailable"}, nil
		}
		var p rpcParameters
		if len(job.Assignment.Parameters) > 8192 || api.StrictDecode(job.Assignment.Parameters, &p) != nil || p.validate() != nil {
			return api.Outcome{Status: "noData", Diagnostic: "invalid DAO check parameters"}, nil
		}
		var chain string
		if err := rpcCall(ctx, client, credentials, "eth_chainId", []any{}, &chain); err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "chain ID request failed"}, nil
		}
		actual, err := quantity(chain)
		expected, _ := quantity(p.ExpectedChainID)
		if err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "RPC returned an invalid chain ID"}, nil
		}
		if actual != expected {
			return api.Outcome{Status: "failure", Diagnostic: "RPC is connected to a different chain"}, nil
		}
		var syncing json.RawMessage
		if err := rpcCall(ctx, client, credentials, "eth_syncing", []any{}, &syncing); err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "sync status request failed"}, nil
		}
		if !bytes.Equal(bytes.TrimSpace(syncing), []byte("false")) {
			var progress map[string]json.RawMessage
			if json.Unmarshal(syncing, &progress) != nil || len(progress) == 0 {
				return api.Outcome{Status: "noData", Diagnostic: "RPC returned an invalid sync status"}, nil
			}
			return api.Outcome{Status: "failure", Diagnostic: "execution client is syncing"}, nil
		}
		var block struct {
			Number    string `json:"number"`
			Timestamp string `json:"timestamp"`
		}
		if err := rpcCall(ctx, client, credentials, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "latest block request failed"}, nil
		}
		timestamp, err := quantity(block.Timestamp)
		if err != nil || timestamp > 1<<62 {
			return api.Outcome{Status: "noData", Diagnostic: "RPC returned an invalid block timestamp"}, nil
		}
		if _, err = quantity(block.Number); err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "RPC returned an invalid block number"}, nil
		}
		age := now().Sub(time.Unix(int64(timestamp), 0))
		if age < -15*time.Second {
			return api.Outcome{Status: "noData", Diagnostic: "block timestamp is in the future; check worker clock"}, nil
		}
		if age > time.Duration(p.MaxBlockAge)*time.Second {
			return api.Outcome{Status: "failure", Diagnostic: "RPC head is older than the configured threshold"}, nil
		}
		var code string
		// Use the inspected height instead of a moving latest selector. A
		// reorganization can still replace the block at that height.
		if err = rpcCall(ctx, client, credentials, "eth_getCode", []any{p.Governor, block.Number}, &code); err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "governor bytecode request failed"}, nil
		}
		decoded, err := hexBytes(code)
		if err != nil {
			return api.Outcome{Status: "noData", Diagnostic: "RPC returned invalid contract bytecode"}, nil
		}
		if len(decoded) == 0 {
			return api.Outcome{Status: "failure", Diagnostic: "governor has no code at the inspected block"}, nil
		}
		data, _ := json.Marshal(struct {
			ChainID         string `json:"chainID"`
			BlockNumber     string `json:"blockNumber"`
			BlockAgeSeconds int64  `json:"blockAgeSeconds"`
		}{chain, block.Number, max(0, int64(age.Seconds()))})
		return api.Outcome{Status: "success", Diagnostic: "expected chain, synced execution client, recent head, and governor code observed", Data: data}, nil
	}
}

// internalSMS defines this example gateway's contract. 202 plus a bounded
// acceptance ID proves gateway acceptance, not handset delivery. Network errors,
// redirects, 5xx and malformed acceptance replies have an uncertain outcome.
func internalSMS(client *http.Client) worker.Handler {
	return func(parent context.Context, job worker.Job) (api.Outcome, error) {
		ctx, cancel := context.WithTimeout(parent, 4*time.Second)
		defer cancel()
		credentials, ok := job.Credentials.(smsCredentials)
		if !ok || credentials.URL == "" || credentials.Token == "" {
			return api.Outcome{Status: "rejected", RejectionCode: "invalid_configuration", Diagnostic: "SMS credential profile is unavailable"}, nil
		}
		var p smsParameters
		if len(job.Assignment.Parameters) > 8192 || api.StrictDecode(job.Assignment.Parameters, &p) != nil || p.validate() != nil {
			return api.Outcome{Status: "rejected", RejectionCode: "invalid_configuration", Diagnostic: "invalid SMS parameters"}, nil
		}
		recipient, exists := credentials.Recipients[p.RecipientAlias]
		if !exists || recipient == "" || len(recipient) > 64 {
			return api.Outcome{Status: "rejected", RejectionCode: "invalid_configuration", Diagnostic: "recipient alias is not configured on this worker"}, nil
		}
		if job.Assignment.ExecutionID == "" || len(job.Assignment.ExecutionID) > 512 {
			return api.Outcome{Status: "rejected", RejectionCode: "invalid_configuration", Diagnostic: "execution identity is missing or invalid"}, nil
		}
		body, err := json.Marshal(struct {
			To          string `json:"to"`
			Text        string `json:"text"`
			ExecutionID string `json:"executionID"`
		}{recipient, p.Text, job.Assignment.ExecutionID})
		if err != nil {
			return api.Outcome{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.URL, bytes.NewReader(body))
		if err != nil {
			return api.Outcome{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+credentials.Token)
		// Correlation is in the body. No automatic HTTP mutation retry is enabled;
		// the encrypted outbox retries delivery of this outcome to CPRa only.
		response, err := client.Do(req)
		if err != nil {
			return api.Outcome{Status: "unknown", Diagnostic: "SMS request outcome is unknown; do not resend automatically"}, nil
		}
		defer response.Body.Close()
		if response.StatusCode == 400 || response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 422 {
			return api.Outcome{Status: "rejected", RejectionCode: "gateway_rejected", Diagnostic: "SMS gateway rejected the request before acceptance"}, nil
		}
		if response.StatusCode != http.StatusAccepted {
			return api.Outcome{Status: "unknown", Diagnostic: "SMS gateway did not return the documented acceptance response"}, nil
		}
		raw, err := readBounded(response.Body, 8192)
		if err != nil {
			return api.Outcome{Status: "unknown", Diagnostic: "SMS acceptance response could not be read"}, nil
		}
		var acceptance struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if api.StrictDecode(raw, &acceptance) != nil || acceptance.Status != "accepted" || len(acceptance.ID) < 1 || len(acceptance.ID) > 128 || strings.ContainsAny(acceptance.ID, "\r\n") {
			return api.Outcome{Status: "unknown", Diagnostic: "SMS acceptance response is invalid"}, nil
		}
		data, _ := json.Marshal(struct {
			GatewayAcceptanceID string `json:"gatewayAcceptanceID"`
		}{acceptance.ID})
		return api.Outcome{Status: "accepted", Diagnostic: "internal gateway accepted the SMS; handset delivery is not verified", Data: data}, nil
	}
}
