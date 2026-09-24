package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManagementEventsUseBoundedV2HistoryAndPreserveAuditFields(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v2/history" || r.URL.Query().Get("monitorID") != "service-api" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cursor") != "original-cursor" {
			t.Error("event read changed exact monitor, cursor or bounded SDK route")
		}
		_ = json.NewEncoder(w).Encode(api.EventList{Items: []api.Event{{ID: "event-1", MonitorID: "service-api", Kind: "action_reviewed", ActionID: "action-1", Actor: "team/oncall", Outcome: "inconclusive", Reason: "Awaiting evidence", Note: "Investigating deployment", EvidenceRefs: []string{"ticket:123"}}}, NextCursor: "next-page"})
	})
	for _, output := range []string{"json", "yaml", "table", "wide"} {
		stdout, stderr, err := fixture.run(t, nil, "get", "events", "--monitor-id", "service-api", "--cursor", "original-cursor", "-o", output)
		if err != nil || !strings.Contains(stdout, "team/oncall") || !strings.Contains(stdout, "Investigating deployment") || !strings.Contains(stdout, "ticket:123") || !strings.Contains(stdout, "inconclusive") {
			t.Fatalf("audit fields lost: %v", err)
		}
		if output == "json" {
			var page api.EventList
			if json.Unmarshal([]byte(stdout), &page) != nil || page.NextCursor != "next-page" || len(page.Items) != 1 {
				t.Fatal("event JSON is not the standalone canonical response")
			}
		} else if output == "table" || output == "wide" {
			if !strings.Contains(stderr, "next-page") || strings.Contains(stdout, "Next cursor:") {
				t.Fatal("table cursor lost or placed in canonical row output")
			}
		}
	}
	if requests.Load() != 4 {
		t.Fatal("event observation automatically collected another page")
	}
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	command, _, err := root.Find([]string{"get", "history"})
	if err != nil || command.Name() != "history" {
		t.Fatal("history command is missing")
	}
}

func TestManagementEventsRejectFleetReadsAndPropagateExpiration(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(api.Problem{Status: 410, Detail: cliSecretValue})
	})
	for _, args := range [][]string{
		{"get", "events"},
		{"get", "events", "service-api"},
		{"get", "events", "--monitor-id", "service/api"},
		{"get", "events", "--monitor-id", "service-api", "--limit", "501"},
		{"get", "events", "--monitor-id", "service-api", "--limit", "0"},
		{"get", "events", "--monitor-id", "service-api", "--all"},
		{"get", "events", "--monitor-id", "service-api", "--action-id", "a"},
	} {
		if stdout, _, err := fixture.run(t, nil, args...); err == nil || stdout != "" {
			t.Fatal("unbounded/unsupported event read accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid event read caused request")
	}
	stdout, stderr, err := fixture.run(t, nil, "get", "events", "--monitor-id", "service-api", "--cursor", "expired")
	if !errors.Is(err, cpra.ErrExpired) || requests.Load() != 1 || stdout != "" || strings.Contains(stderr+err.Error(), cliSecretValue) {
		t.Fatal("expired event cursor retried, fell back to v1 or exposed response detail")
	}
}

func TestManagementActionDescribeQuotesUnknownReviewControls(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.Action{ID: "action-1", State: "unknown", Review: &api.ActionReview{Resolution: api.ActionReviewResolution("future\x1b[31m\nassertion")}})
	})
	stdout, _, err := fixture.run(t, nil, "describe", "action/action-1")
	if err != nil || strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, `\x1b[31m\nassertion`) {
		t.Fatal("unknown enum controls reached terminal output unquoted", err)
	}
}
