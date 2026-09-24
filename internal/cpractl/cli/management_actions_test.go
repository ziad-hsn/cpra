package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func actionAuditFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-input")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagementRecoveryAndReviewUseExactDistinctVersions(t *testing.T) {
	note := actionAuditFile(t, "Verified independently")
	evidence := actionAuditFile(t, `["ticket:123","receipt:456"]`)
	for _, action := range []string{"recover", "review"} {
		t.Run(action, func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				path, revision := "/api/v2/monitors/service-api/recover", "observed-control"
				expected := api.ControlRequest{Revision: revision, Reason: "Investigated target"}
				if action == "review" {
					path, revision = "/api/v2/actions/unknown-action/review", "observed-review"
					expected = api.ControlRequest{Revision: revision, Reason: "Investigated target", Resolution: "accepted", Note: "Verified independently", EvidenceRefs: []string{"ticket:123", "receipt:456"}}
				}
				var got api.ControlRequest
				if r.Method != "POST" || r.URL.Path != path || r.Header.Get("If-Match") != `"`+revision+`"` || r.Header.Get("Authorization") != "Bearer named-operator-token" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, expected) {
					t.Error("control changed its explicit version, audit content or SDK operation")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Operation-ID", cliOperationID)
				if action == "recover" {
					_ = json.NewEncoder(w).Encode(api.Operation{ID: cliOperationID, ContentDigest: strings.Repeat("a", 64), State: "committed", Committed: api.Pointer(int64(1)), Applied: api.Pointer(int64(0))})
				} else {
					_ = json.NewEncoder(w).Encode(api.Action{ID: "unknown-action", State: "unknown", Outcome: "external_outcome_unknown", ReviewRevision: "new-review", Review: &api.ActionReview{Actor: "team/oncall", Resolution: api.Accepted, Reason: got.Reason}})
				}
			})
			args := []string{"recover", "monitor/service-api", "--control-revision", "observed-control", "--reason-file", "-", "-o", "json"}
			if action == "review" {
				args = []string{"review", "action/unknown-action", "--review-revision", "observed-review", "--reason-file", "-", "--resolution", "accepted", "--note-file", note, "--evidence-file", evidence, "-o", "json"}
			}
			stdout, stderr, err := fixture.run(t, []byte("Investigated target"), args...)
			var got map[string]any
			if err != nil || requests.Load() != 1 || json.Unmarshal([]byte(stdout), &got) != nil || !strings.Contains(stderr, cliOperationID) || strings.Contains(stdout, "Operation:") {
				t.Fatalf("conditional control lost receipt, changed stdout or prefetched: %v", err)
			}
			if action == "review" && (got["state"] != "unknown" || got["outcome"] != "external_outcome_unknown") {
				t.Fatal("operator assertion overwrote provider facts in output")
			}
			if action == "recover" {
				var operation api.Operation
				if json.Unmarshal([]byte(stdout), &operation) != nil || operation.State != "committed" || (operation.Committed == nil || *operation.Committed != 1) || (operation.Applied == nil || *operation.Applied != 0) {
					t.Fatal("admission receipt fabricated controller application")
				}
			}
		})
	}
}

func TestManagementActionControlsRejectInputsWithoutRequests(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, args := range [][]string{
		{"recover", "monitor/service-api", "--control-revision", "v"},
		{"recover", "monitor/service-api", "--resource-version", "v", "--reason-file", "-"},
		{"recover", "monitor/service-api", "--control-revision", `"v"`, "--reason-file", "-"},
		{"recover", "incident/current", "--control-revision", "v", "--reason-file", "-"},
		{"recover", "monitor/service-api", "--control-revision", "v", "--reason-file", "-", "--all"},
		{"recover", "monitor/service-api", "--control-revision", "v", "--reason-file", "-", "--note-file", "-"},
		{"recover", "monitor/service-api", "monitor/another", "--control-revision", "v", "--reason-file", "-"},
		{"review", "action/a", "--review-revision", "v", "--reason-file", "-"},
		{"review", "action/a", "--review-revision", "v", "--reason-file", "-", "--resolution", "succeeded"},
		{"review", "action/a", "--revision", "v", "--reason-file", "-", "--resolution", "accepted"},
		{"review", "monitor/a", "--review-revision", "v", "--reason-file", "-", "--resolution", "accepted"},
		{"review", "action/a", "--review-revision", "v", "--reason-file", "-", "--resolution", "accepted", "--note-file", "-"},
		{"review", "action/a", "--review-revision", "v", "--reason-file", "-", "--resolution", "accepted", "--evidence-file", "-"},
		{"review", "action/a", "--review-revision", "v", "--reason-file", "-", "--resolution", "accepted", "--actor", "someone-else"},
		{"get", "actions", "--limit", "501"},
		{"get", "actions", "a", "--monitor-id", "m"},
		{"get", "actions", "--selector", "team=all"},
	} {
		if stdout, _, err := fixture.run(t, []byte("Investigated"), args...); err == nil || stdout != "" {
			t.Fatalf("invalid %s input accepted", args[0])
		}
	}
	for _, input := range []string{`null`, `{}`, `[null]`, `["duplicate","duplicate"]`, `[""]`, `["bad\nref"]`, `["a"] ["b"]`, "[\"" + strings.Repeat("x", 2049) + "\"]", "[\"" + strings.Repeat("x", 128*1024) + "\"]", `["1","2","3","4","5","6","7","8","9"]`, string([]byte{'[', '"', 0xff, '"', ']'})} {
		path := actionAuditFile(t, input)
		stdout, stderr, err := fixture.run(t, []byte("Investigated"), "review", "action/a", "--review-revision", "v", "--resolution", "inconclusive", "--reason-file", "-", "--evidence-file", path)
		if err == nil || stdout != "" || strings.Contains(stderr, input) {
			t.Fatal("invalid evidence accepted or echoed")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid action control caused a request")
	}
}

func TestManagementReviewErrorsKeepTypeAndNeverRetry(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{403, cpra.ErrUnauthorized}, {412, cpra.ErrConflict}, {0, cpra.ErrAmbiguous}} {
		var requests atomic.Int32
		fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if test.status == 0 {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				connection.Close()
				return
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(test.status)
			_ = json.NewEncoder(w).Encode(api.Problem{Status: int64(test.status), Detail: cliSecretValue})
		})
		stdout, stderr, err := fixture.run(t, []byte("Awaiting receipt"), "review", "action/a", "--review-revision", "observed", "--resolution", "inconclusive", "--reason-file", "-")
		if !errors.Is(err, test.want) || requests.Load() != 1 || stdout != "" || strings.Contains(stderr+err.Error(), cliSecretValue) {
			t.Fatalf("review lost typed error, leaked detail or retried: %v", err)
		}
	}
}

func TestManagementActionReadsKeepProviderAndReviewSeparate(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		action := api.Action{ID: "original", MonitorID: "service-api", State: "unknown", Outcome: "external_outcome_unknown", ReviewRevision: "observed-action", ExecutorFenced: true, Review: &api.ActionReview{Actor: "team/oncall", Resolution: api.Accepted, Reason: "Provider receipt verified"}}
		if r.Method != "GET" {
			t.Error("action observation caused a mutation")
		}
		if r.URL.Path == "/api/v2/actions" {
			if r.URL.Query().Get("monitorID") != "service-api" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cursor") != "original-cursor" {
				t.Error("action paging lost exact input")
			}
			_ = json.NewEncoder(w).Encode(api.ActionList{Items: []api.Action{action}, NextCursor: "next-page"})
		} else if r.URL.Path == "/api/v2/actions/original" {
			_ = json.NewEncoder(w).Encode(action)
		} else {
			t.Error("unexpected action route")
		}
	})
	for _, args := range [][]string{
		{"get", "action/original", "-o", "json"},
		{"get", "actions", "original", "-o", "wide"},
		{"describe", "action/original"},
		{"get", "actions", "--monitor-id", "service-api", "--cursor", "original-cursor", "-o", "json"},
	} {
		stdout, _, err := fixture.run(t, nil, args...)
		if err != nil || !strings.Contains(stdout, "unknown") || !strings.Contains(stdout, "accepted") || !strings.Contains(stdout, "observed-action") {
			t.Fatalf("action facts/version/independent review missing: %v", err)
		}
		if args[0] == "describe" && (!strings.Contains(stdout, "unavailable") || strings.Contains(stdout, "0001-01-01")) {
			t.Fatal("unavailable action creation time fabricated")
		}
	}
	if requests.Load() != 4 {
		t.Fatal("action read implicitly collected another page or fetched evidence")
	}
}
