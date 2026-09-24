package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManagementControlsUseExactVersionsAndSDKOperations(t *testing.T) {
	for _, test := range []struct {
		action, target, versionFlag, path, body string
		extra                                   []string
	}{
		{"acknowledge", "incident/current", "revision", "/incidents/current/acknowledge", `{"revision":"observed-version","note":"Investigating"}`, []string{"--note-file", "-"}},
		{"dismiss", "incident/current", "revision", "/incidents/current/dismiss", `{"revision":"observed-version","reason":"Investigating"}`, []string{"--reason-file", "-"}},
		{"reopen", "incident/current", "revision", "/incidents/current/reopen", `{"revision":"observed-version"}`, nil},
		{"snooze", "monitor/service-api", "control-revision", "/monitors/service-api/snooze", `{"revision":"observed-version","duration":"30m","reason":"Investigating"}`, []string{"--reason-file", "-", "--for", "30m"}},
		{"unsnooze", "monitor/service-api", "control-revision", "/monitors/service-api/unsnooze", `{"revision":"observed-version"}`, nil},
		{"disable", "monitor/service-api", "resource-version", "/monitors/service-api", `{"spec":{"enabled":false}}`, nil},
		{"enable", "monitor/service-api", "resource-version", "/monitors/service-api", `{"spec":{"enabled":true}}`, nil},
	} {
		t.Run(test.action, func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				method, contentType := "POST", "application/json"
				if test.action == "disable" || test.action == "enable" {
					method, contentType = "PATCH", "application/merge-patch+json"
				}
				if r.Method != method || r.URL.Path != "/api/v2"+test.path || r.Header.Get("If-Match") != `"observed-version"` || r.Header.Get("Content-Type") != contentType || r.Header.Get("Authorization") != "Bearer named-operator-token" {
					t.Error("wrong conditional authenticated control request")
				}
				var actual, expected map[string]any
				if json.NewDecoder(r.Body).Decode(&actual) != nil || json.Unmarshal([]byte(test.body), &expected) != nil || !reflect.DeepEqual(actual, expected) {
					t.Error("control changed revision, enabled value, comment or requested duration")
				}
				w.Header().Set("X-Operation-ID", cliOperationID)
				w.Header().Set("Content-Type", "application/json")
				switch test.action {
				case "snooze", "unsnooze":
					_ = json.NewEncoder(w).Encode(api.Operation{ID: cliOperationID, ContentDigest: strings.Repeat("a", 64), State: "committed", Committed: api.Pointer(int64(1))})
				case "enable", "disable":
					_ = json.NewEncoder(w).Encode(cliResource("Monitor", "service-api"))
				default:
					_ = json.NewEncoder(w).Encode(api.Incident{ID: "current", MonitorID: "service-api", Revision: "new-version", State: "open", AcknowledgedBy: "authenticated-operator"})
				}
			})
			args := append([]string{test.action, test.target, "--" + test.versionFlag, "observed-version", "-o", "json"}, test.extra...)
			stdout, stderr, err := fixture.run(t, []byte("Investigating"), args...)
			var response map[string]any
			if err != nil || json.Unmarshal([]byte(stdout), &response) != nil || requests.Load() != 1 {
				t.Fatalf("single control SDK operation failed or prefetched: %v", err)
			}
			if !strings.Contains(stderr, cliOperationID) || strings.Contains(stdout, "Operation:") {
				t.Fatal("operation receipt was lost or corrupted canonical stdout")
			}
		})
	}
}

func TestManagementControlsRejectInvalidInputsBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, args := range [][]string{
		{"acknowledge", "incident/current"},
		{"acknowledge", "monitor/service-api", "--revision", "v"},
		{"dismiss", "incident/current", "--revision", "v"},
		{"dismiss", "incident/current", "--revision", `"v"`, "--reason-file", "does-not-exist"},
		{"snooze", "monitor/service-api", "--control-revision", "v", "--duration", "0s", "--reason-file", "-"},
		{"snooze", "monitor/service-api", "--control-revision", "v", "--duration", "-1s", "--reason-file", "-"},
		{"snooze", "monitor/service-api", "--control-revision", "v", "--duration", "721h", "--reason-file", "-"},
		{"snooze", "monitor/service-api", "--control-revision", "v", "--duration", "1h", "--for", "1h", "--reason-file", "-"},
		{"snooze", "monitor/service-api", "--resource-version", "v", "--for", "1h", "--reason-file", "-"},
		{"unsnooze", "incident/current", "--control-revision", "v"},
		{"disable", "monitor/service-api", "--resource-version", "v", "--selector", "team=all"},
		{"enable", "monitor/service-api", "--resource-version", "v", "--all"},
		{"disable", "monitor/service-api", "monitor/second", "--resource-version", "v"},
		{"enable", "monitor", "--resource-version", "v"},
		{"acknowledge", "incident/current", "--revision", "v", "--actor", "spoofed"},
	} {
		if stdout, _, err := fixture.run(t, []byte("Scheduled maintenance"), args...); err == nil || stdout != "" {
			t.Fatalf("invalid %s control accepted", args[0])
		}
	}
	for _, text := range [][]byte{nil, []byte(" \n\t "), []byte(strings.Repeat("x", 4097)), {0xff}, []byte("NUL\x00text"), []byte("CR\rtext")} {
		if _, _, err := fixture.run(t, text, "dismiss", "incident/current", "--revision", "v", "--reason-file", "-"); err == nil {
			t.Fatal("invalid operator reason accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid control caused an HTTP request")
	}
}

func TestManagementControlConflictAndLostReplyDoNotRetry(t *testing.T) {
	for _, lost := range []bool{false, true} {
		var requests atomic.Int32
		fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if lost {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				connection.Close()
				return
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusPreconditionFailed)
			_ = json.NewEncoder(w).Encode(api.Problem{Status: 412, Code: "preconditionFailed", Detail: cliSecretValue})
		})
		stdout, stderr, err := fixture.run(t, nil, "acknowledge", "incident/current", "--revision", "observed")
		want := cpra.ErrConflict
		if lost {
			want = cpra.ErrAmbiguous
		}
		if !errors.Is(err, want) || requests.Load() != 1 || stdout != "" || strings.Contains(stderr+err.Error(), cliSecretValue) {
			t.Fatalf("control error lost type, exposed detail or caused retry: %v", err)
		}
	}
}

func TestManagementIncidentReadsProvideVersionsWithoutChangingLegacy(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		incident := api.Incident{ID: "current", MonitorID: "service-api", Revision: "incident-version", State: "open", AcknowledgedBy: "Alex"}
		if r.URL.Path == "/api/v2/incidents" {
			if r.URL.Query().Get("monitorID") != "service-api" || r.URL.Query().Get("limit") != "100" {
				t.Error("incident page lost exact monitor selection or limit")
			}
			_ = json.NewEncoder(w).Encode(api.IncidentList{Items: []api.Incident{incident}, NextCursor: "next-page"})
			return
		}
		if r.URL.Path != "/api/v2/incidents/current" || r.Method != "GET" {
			t.Error("wrong incident read route")
		}
		_ = json.NewEncoder(w).Encode(incident)
	})
	for _, args := range [][]string{
		{"get", "incident/current", "-o", "json"},
		{"describe", "incident/current"},
		{"get", "incident-records", "current", "-o", "wide"},
		{"get", "incident-records", "--monitor-id", "service-api", "-o", "json"},
	} {
		stdout, _, err := fixture.run(t, nil, args...)
		if err != nil || !strings.Contains(stdout, "incident-version") || !strings.Contains(stdout, "Alex") {
			t.Fatalf("incident revision/attention unavailable: %v", err)
		}
	}
	if requests.Load() != 4 {
		t.Fatal("incident navigation implicitly requested another page")
	}
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	command, _, err := root.Find([]string{"get", "incidents"})
	if err != nil || command.Name() != "incidents" {
		t.Fatal("incident command is missing")
	}
}
