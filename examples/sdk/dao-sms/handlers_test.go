//go:build externaljobs

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/worker"
)

func checkParameters(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(rpcParameters{ExpectedChainID: "0x1", Governor: "0x1111111111111111111111111111111111111111", MaxBlockAge: 120})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDAOHealthOverHTTP(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, test := range []struct{ name, want string }{
		{"healthy", "success"}, {"wrong_chain", "failure"}, {"syncing", "failure"}, {"stale", "failure"}, {"no_code", "failure"}, {"future_timestamp", "noData"}, {"rpc_error", "noData"}, {"wrong_id", "noData"}, {"oversized", "noData"}, {"bad_hex", "noData"}, {"bad_sync", "noData"}, {"bad_code", "noData"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer local-secret" {
					t.Error("missing worker-local credential")
				}
				var request struct {
					Method string `json:"method"`
					Params []any  `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad request")
					return
				}
				if test.name == "oversized" {
					_, _ = w.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
					return
				}
				id := 1
				if test.name == "wrong_id" {
					id = 2
				}
				if test.name == "rpc_error" {
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "local-secret provider detail"}})
					return
				}
				var result any
				switch request.Method {
				case "eth_chainId":
					result = "0x1"
					if test.name == "wrong_chain" {
						result = "0xa"
					}
					if test.name == "bad_hex" {
						result = "0x01"
					}
				case "eth_syncing":
					result = false
					if test.name == "syncing" {
						result = map[string]string{"startingBlock": "0x0", "currentBlock": "0x1", "highestBlock": "0x2"}
					}
					if test.name == "bad_sync" {
						result = true
					}
				case "eth_getBlockByNumber":
					if len(request.Params) != 2 || request.Params[0] != "latest" || request.Params[1] != false {
						t.Error("block should request latest with hashes only")
					}
					stamp := now.Add(-5 * time.Second)
					if test.name == "stale" {
						stamp = now.Add(-121 * time.Second)
					}
					if test.name == "future_timestamp" {
						stamp = now.Add(time.Minute)
					}
					result = map[string]any{"number": "0x123", "timestamp": "0x" + strconv.FormatInt(stamp.Unix(), 16), "transactions": []string{}}
				case "eth_getCode":
					if len(request.Params) != 2 || request.Params[1] != "0x123" {
						t.Error("code lookup must use the inspected block")
					}
					result = "0x6001"
					if test.name == "no_code" {
						result = "0x"
					}
					if test.name == "bad_code" {
						result = "0x0"
					}
				default:
					t.Errorf("unexpected method %s", request.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			}))
			defer server.Close()
			client := providerClient()
			defer client.CloseIdleConnections()
			result, err := daoHealth(client, func() time.Time { return now })(context.Background(), worker.Job{Assignment: api.Assignment{Parameters: checkParameters(t)}, Credentials: rpcCredentials{URL: server.URL, Token: "local-secret"}})
			if err != nil || result.Status != test.want {
				t.Fatalf("status=%s error=%v diagnostic=%s", result.Status, err, result.Diagnostic)
			}
			if strings.Contains(result.Diagnostic, "local-secret") || strings.Contains(string(result.Data), "local-secret") {
				t.Fatal("provider diagnostic leaked")
			}
			if test.name == "healthy" && calls.Load() != 4 {
				t.Fatalf("healthy check made %d calls, want 4", calls.Load())
			}
		})
	}
}

func TestSMSAcceptanceAndAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name       string
		code       int
		body, want string
	}{
		{"accepted", 202, `{"id":"gateway-42","status":"accepted"}`, "accepted"},
		{"unauthorized", 401, `secret provider body`, "rejected"},
		{"invalid_destination", 422, `secret destination`, "rejected"},
		{"overloaded", 429, `secret provider body`, "unknown"},
		{"internal_error", 500, `secret provider body`, "unknown"},
		{"malformed_acceptance", 202, `{`, "unknown"},
		{"unexpected_delivery", 202, `{"id":"x","status":"delivered"}`, "unknown"},
		{"oversized_id", 202, `{"id":"` + strings.Repeat("x", 129) + `","status":"accepted"}`, "unknown"},
		{"oversized_body", 202, strings.Repeat("x", 8193), "unknown"},
		{"redirect", 307, ``, "unknown"},
		{"lost_response", 0, ``, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer sms-local-token" {
					t.Error("missing SMS credential")
				}
				var request map[string]string
				if json.NewDecoder(r.Body).Decode(&request) != nil || request["to"] != "private-recipient" || request["executionID"] != "action-7" {
					t.Error("invalid SMS request")
				}
				if test.code == 0 {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				if test.code == 307 {
					w.Header().Set("Location", "/credential-trap")
				}
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := providerClient()
			defer client.CloseIdleConnections()
			parameters, _ := json.Marshal(smsParameters{RecipientAlias: "dao-oncall", Text: "DAO RPC requires attention"})
			result, err := internalSMS(client)(context.Background(), worker.Job{Assignment: api.Assignment{ExecutionID: "action-7", Parameters: parameters}, Credentials: smsCredentials{URL: server.URL, Token: "sms-local-token", Recipients: map[string]string{"dao-oncall": "private-recipient"}}})
			if err != nil || result.Status != test.want {
				t.Fatalf("got status=%s error=%v", result.Status, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("notification sent %d requests", calls.Load())
			}
			raw, _ := json.Marshal(result)
			for _, secret := range []string{"sms-local-token", "private-recipient", "secret provider body"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("outcome leaked %s", secret)
				}
			}
		})
	}
}

func TestAssignmentCannotRedirectProvider(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := providerClient()
	defer client.CloseIdleConnections()
	parameters := bytes.Replace(checkParameters(t), []byte(`"governor":`), []byte(`"url":"https://attacker.invalid","governor":`), 1)
	result, err := daoHealth(client, time.Now)(context.Background(), worker.Job{Assignment: api.Assignment{Parameters: parameters}, Credentials: rpcCredentials{URL: server.URL}})
	if err != nil || result.Status != "noData" || calls.Load() != 0 {
		t.Fatal("unrecognized destination field caused a request")
	}
}

func TestResourcesHaveCompleteLocalReferencesAndNoSecrets(t *testing.T) {
	items, err := resources(rpcParameters{ExpectedChainID: "0x1", Governor: "0x1111111111111111111111111111111111111111", MaxBlockAge: 120})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := collection.FreezeResources(context.Background(), collection.Slice(items), collection.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	if _, err = collection.ValidateReferences(context.Background(), frozen, nil); err != nil {
		t.Fatal(err)
	}
	if frozen.Len() != 5 {
		t.Fatalf("got %d resources", frozen.Len())
	}
	raw, _ := json.Marshal(items)
	for _, forbidden := range []string{"tokenFile", "recipients", "wrapping.key", "https://"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("registration unexpectedly contains %s", forbidden)
		}
	}
}

func TestProviderURLPolicy(t *testing.T) {
	for _, test := range []struct {
		address     string
		allow, want bool
	}{
		{"https://rpc.example.test/rpc", false, true}, {"http://127.0.0.1:123/rpc", true, true}, {"http://[::1]:123/rpc", true, true},
		{"http://127.0.0.1/rpc", false, false}, {"http://localhost/rpc", true, false}, {"http://10.0.0.1/rpc", true, false},
		{"https://user:secret@rpc.example.test/rpc", false, false}, {"https://rpc.example.test/rpc?key=secret", false, false}, {"https://rpc.example.test/rpc#secret", false, false},
	} {
		if got := validateProviderURL(test.address, test.allow) == nil; got != test.want {
			t.Errorf("URL %s accepted=%v, want %v", test.address, got, test.want)
		}
	}
}

func TestLocalTokenReadIsBounded(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	for _, contents := range []string{"", "abc\nsecret", strings.Repeat("x", 16385), "abc\x00secret"} {
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readToken(file, true); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestDemoRegistersAndExecutesWithDurableWorker(t *testing.T) {
	var output bytes.Buffer
	if err := demo(context.Background(), &output); err != nil {
		t.Fatal(err, output.String())
	}
	for _, line := range []string{"Registered 2 JobTypes", "check: success", "check: failure", "notification: accepted", "RPC requests: 7; SMS requests: 1; outcome submissions: 4", "Encrypted outbox drained"} {
		if !strings.Contains(output.String(), line) {
			t.Errorf("demo omitted %q: %s", line, output.String())
		}
	}
}
