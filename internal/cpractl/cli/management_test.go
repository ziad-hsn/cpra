package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

const cliOperationID = "c47948b9-084b-4109-a862-0312e7a77102"
const cliSecretValue = "credential-value-that-must-not-reach-output"

func cliResource(kind, id string) api.Resource {
	var spec any
	switch kind {
	case "Monitor":
		spec = api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}}
	case "NotificationEndpoint":
		spec = api.DriverConfig{Type: "webhook", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "hook"})}
	case "Recipient":
		spec = api.RecipientSpec{EndpointRefs: []string{"hook"}}
	case "NotificationGroup":
		spec = api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}
	case "Credential":
		spec = api.CredentialSpec{Value: api.Pointer(cliSecretValue)}
	}
	raw, _ := json.Marshal(spec)
	return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id}, Spec: raw}
}

type cliManagementFixture struct {
	server    *httptest.Server
	ca, token string
}

func newCLIManagementFixture(t *testing.T, handler http.HandlerFunc) cliManagementFixture {
	t.Helper()
	t.Setenv("CPRA_AUTH_TOKEN", "")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	t.Setenv("CPRA_CA_FILE", "")
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("named-operator-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return cliManagementFixture{server, ca, token}
}
func (f cliManagementFixture) run(t *testing.T, input []byte, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(bytes.NewReader(input))
	root.SetArgs(append([]string{"--server", f.server.URL, "--ca-file", f.ca, "--token-file", f.token}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestManagementCRUDUsesPublicSDKForEveryResource(t *testing.T) {
	for _, entry := range []struct{ kind, alias, path string }{
		{"Monitor", "monitor", "monitors"}, {"NotificationEndpoint", "endpoint", "notification-endpoints"}, {"Recipient", "recipient", "recipients"}, {"NotificationGroup", "group", "notification-groups"}, {"Credential", "secret", "credentials"},
	} {
		t.Run(entry.kind, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			resource := cliResource(entry.kind, "service-a")
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				methods = append(methods, r.Method)
				if r.Header.Get("Authorization") != "Bearer named-operator-token" {
					t.Error("named authentication missing")
				}
				wantPath := "/api/v2/" + entry.path
				if r.Method != "POST" {
					wantPath += "/service-a"
				}
				if r.URL.Path != wantPath {
					t.Error("wrong resource route")
				}
				if r.Method == "POST" && r.Header.Get("If-None-Match") != "*" {
					t.Error("create absence precondition missing")
				}
				if r.Method == "PUT" || r.Method == "PATCH" || r.Method == "DELETE" {
					if r.Header.Get("If-Match") != `"revision-1"` {
						t.Error("strong version precondition missing")
					}
				}
				if r.Method == "PATCH" && r.Header.Get("Content-Type") != "application/merge-patch+json" {
					t.Error("wrong merge patch media type")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Operation-ID", cliOperationID)
				w.Header().Set("ETag", `"revision-1"`)
				if r.Method == "DELETE" {
					_ = json.NewEncoder(w).Encode(api.Operation{ID: cliOperationID, ContentDigest: strings.Repeat("a", 64), State: "committed", Committed: api.Pointer(int64(1))})
					return
				}
				response := resource
				response.Metadata.UID = "uid-1"
				response.Metadata.ResourceVersion = "revision-1"
				response.Metadata.Generation = 1
				if entry.kind == "Credential" {
					response.Status = json.RawMessage(fmt.Sprintf(`{"available":true,"value":%q}`, cliSecretValue))
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			raw, _ := json.Marshal(resource)
			for _, command := range [][]string{
				{"create", entry.alias, "-f", "-", "-o", "json"},
				{"get", entry.alias + "/service-a", "-o", "json"},
				{"describe", entry.alias + "/service-a", "-o", "yaml"},
				{"replace", entry.alias + "/service-a", "-f", "-", "--resource-version", "revision-1", "-o", "json"},
				{"patch", entry.alias + "/service-a", "--patch-file", "-", "--resource-version", "revision-1", "-o", "json"},
				{"delete", entry.alias + "/service-a", "--resource-version", "revision-1", "-o", "json"},
			} {
				input := raw
				if command[0] == "patch" {
					input = []byte(`{"metadata":{"name":"Changed"}}`)
				}
				stdout, stderr, err := fixture.run(t, input, command...)
				if err != nil {
					t.Fatalf("%s failed: %v", command[0], err)
				}
				if strings.Contains(stdout, cliSecretValue) || strings.Contains(stderr, cliSecretValue) {
					t.Fatal("write-only credential echoed to output")
				}
				var document map[string]any
				if command[0] == "describe" {
					if err := yaml.Unmarshal([]byte(stdout), &document); err != nil {
						t.Fatal(err)
					}
				} else if err := json.Unmarshal([]byte(stdout), &document); err != nil {
					t.Fatalf("stdout was not a standalone canonical response: %v", err)
				}
				if command[0] != "delete" && document["apiVersion"] != api.APIVersion {
					t.Fatal("resource output changed canonical field names")
				}
				if entry.kind == "Credential" && command[0] != "delete" {
					if _, exists := document["spec"].(map[string]any)["value"]; exists {
						t.Fatal("credential value key remained visible")
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(methods, []string{"POST", "GET", "GET", "PUT", "PATCH", "DELETE"}) {
				t.Fatalf("CRUD implicitly fetched or retried: %v", methods)
			}
		})
	}
}

func TestManagementMutationRequiresExplicitOriginalVersionBeforeInputOrRequest(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, version := range []string{"", `W/"old"`, `"old"`, "*", "old,new", "old\nnew"} {
		args := []string{"replace", "monitor/api", "-f", "-"}
		if version != "" {
			args = append(args, "--resource-version", version)
		}
		_, _, err := fixture.run(t, []byte("protected malformed input"), args...)
		if err == nil || !strings.Contains(err.Error(), "resource-version") {
			t.Fatalf("bad version accepted or input consumed first: %v", err)
		}
	}
	resource := cliResource("Monitor", "api")
	resource.Metadata.ResourceVersion = "file-version"
	raw, _ := json.Marshal(resource)
	if _, _, err := fixture.run(t, raw, "replace", "monitor/api", "-f", "-", "--resource-version", "different-version"); err == nil {
		t.Fatal("mismatched file version accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid precondition caused a request")
	}
}

func TestManagementRejectsMultipleOrMalformedInputsBeforeMutation(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	first, _ := json.Marshal(cliResource("Monitor", "first"))
	second, _ := json.Marshal(cliResource("Monitor", "second"))
	for _, input := range [][]byte{append(append(append([]byte{}, first...), []byte("\n")...), second...), append(append([]byte{}, first...), []byte("\n{secret-marker")...), []byte(strings.Repeat(" ", api.MaxResourceBytes+1))} {
		stdout, stderr, err := fixture.run(t, input, "create", "monitor", "-f", "-", "-o", "json")
		if err == nil || stdout != "" || strings.Contains(stderr, "secret-marker") || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("invalid collection input submitted or exposed")
		}
	}
	for _, patch := range []string{`{"spec":{"enabled":false,"enabled":true}}`, `[]`, `null`, `{"spec":{}} {"secret-marker":1}`} {
		if _, _, err := fixture.run(t, []byte(patch), "patch", "monitor/first", "--patch-file", "-", "--resource-version", "old"); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid final input caused mutation")
	}
}

func TestManagementTransportErrorsPreserveClassificationAndDoNotRetry(t *testing.T) {
	for _, status := range []int{403, 412, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(api.Problem{Type: "about:blank", Title: "Rejected", Status: int64(status), Code: "rejected", Detail: cliSecretValue})
			})
			raw, _ := json.Marshal(cliResource("Monitor", "api"))
			stdout, stderr, err := fixture.run(t, raw, "replace", "monitor/api", "-f", "-", "--resource-version", "old")
			var problem *cpra.Error
			if err == nil || !errors.As(err, &problem) || problem.StatusCode != status || requests.Load() != 1 || stdout != "" {
				t.Fatalf("error classification or no-retry contract failed: %v", err)
			}
			if strings.Contains(err.Error(), cliSecretValue) || strings.Contains(stderr, cliSecretValue) {
				t.Fatal("server problem detail leaked")
			}
			if status == 403 && !errors.Is(err, cpra.ErrUnauthorized) {
				t.Fatal("permission denial lost its type")
			}
			if status == 412 && !errors.Is(err, cpra.ErrConflict) {
				t.Fatal("conflict lost its type")
			}
		})
	}
}

func TestManagementLostMutationReplyReturnsUncertaintyWithoutRetry(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	raw, _ := json.Marshal(cliResource("Monitor", "api"))
	stdout, _, err := fixture.run(t, raw, "create", "monitor", "-f", "-")
	if !errors.Is(err, cpra.ErrAmbiguous) || requests.Load() != 1 || stdout != "" {
		t.Fatalf("uncertain outcome retried or hidden: %v", err)
	}
}

func TestManagementHTTPSRequiredUnlessOriginExplicitlyOptedIn(t *testing.T) {
	t.Setenv("CPRA_AUTH_TOKEN", "named-operator-token")
	t.Setenv("CPRA_AUTH_TOKEN_FILE", "")
	t.Setenv("CPRA_CA_FILE", "")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(cliResource("Monitor", "api"))
	}))
	defer server.Close()
	for _, allowed := range []bool{false, true} {
		root := NewRootCommand()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		args := []string{"get", "monitor/api", "--server", server.URL}
		if allowed {
			args = append(args, "--allow-insecure-http")
		}
		root.SetArgs(args)
		err := root.Execute()
		if allowed && err != nil {
			t.Fatal(err)
		}
		if !allowed && err == nil {
			t.Fatal("authenticated HTTP accepted by default")
		}
	}
	if requests.Load() != 1 {
		t.Fatal("HTTPS validation did not precede the request")
	}
}

func TestManagementPageIsBoundedAndNeverImplicitlyFollowsCursor(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("limit") != "100" {
			t.Error("wrong default page limit")
		}
		row := cliResource("Credential", "one")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []api.Resource{row}, "nextCursor": "second-page"})
	})
	stdout, stderr, err := fixture.run(t, nil, "get", "secrets", "-o", "json")
	if err != nil || requests.Load() != 1 || strings.Contains(stdout, cliSecretValue) || stderr != "" {
		t.Fatalf("bounded canonical page failed: %v", err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(stdout), &result) != nil || result["nextCursor"] != "second-page" {
		t.Fatal("continuation was discarded")
	}
	if _, _, err := fixture.run(t, nil, "get", "secrets", "--limit", "501"); err == nil || requests.Load() != 1 {
		t.Fatal("oversized page submitted")
	}
}

func TestManagementDoesNotIntroduceCheckNowOrInlineSecretFlags(t *testing.T) {
	root := NewRootCommand()
	for _, name := range []string{"check-now", "check"} {
		if command, _, err := root.Find([]string{name}); err == nil && command.Name() == name {
			t.Fatal("check-now command was introduced")
		}
	}
	for _, name := range []string{"create", "patch"} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"value", "secret-value", "patch"} {
			if command.Flags().Lookup(flag) != nil {
				t.Fatal("inline secret-bearing flag was introduced")
			}
		}
	}
}

func TestManagementCredentialRedactionUsesTypedServiceEvenWithoutKind(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		resource := cliResource("Credential", "one")
		resource.Kind = ""
		resource.Status = json.RawMessage(fmt.Sprintf(`{"available":true,"value":%q}`, cliSecretValue))
		if r.URL.Path == "/api/v2/credentials" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []api.Resource{resource}})
			return
		}
		_ = json.NewEncoder(w).Encode(resource)
	})
	for _, args := range [][]string{
		{"get", "secret/one", "-o", "json"},
		{"get", "secrets", "-o", "yaml"},
		{"describe", "secret/one"},
	} {
		stdout, stderr, err := fixture.run(t, nil, args...)
		if err != nil || strings.Contains(stdout+stderr, cliSecretValue) {
			t.Fatalf("credential service trusted the response kind for redaction: %v", err)
		}
	}
}

func TestManagementCanceledInputPreservesContextWithoutOpeningFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{
		{"create", "monitor", "-f", "does-not-exist"},
		{"patch", "monitor/api", "--patch-file", "does-not-exist", "--resource-version", "old"},
	} {
		root := NewRootCommand()
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		if err := root.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled input lost context error or opened input: %v", err)
		}
	}
}
