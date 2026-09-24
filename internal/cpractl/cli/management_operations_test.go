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
	"gopkg.in/yaml.v3"
)

func TestManagementOperationsListUsesOneBoundedTLSPage(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations" || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("monitorID") != "service-api" || r.URL.Query().Get("cursor") != "original-cursor" || r.URL.Query().Get("selector") != "" {
			t.Error("operation list changed the original bounded filter/cursor or method")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"`+sequencedOperationID+`","state":"reserved","contentDigest":"digest"},{"id":"`+cliOperationID+`","state":"failed","contentDigest":"digest","uploaded":0,"committed":0,"applied":0,"validated":false,"items":[{"id":"Monitor/service-api","outcome":"activation_rejected","committed":false,"applied":false}]}],"nextCursor":"next-page","snapshot":"snapshot-identity"}`)
	})
	for _, output := range []string{"table", "wide", "json", "yaml"} {
		stdout, stderr, err := fixture.run(t, nil, "get", "operations", "--monitor-id", "service-api", "--cursor", "original-cursor", "-o", output)
		if err != nil || !strings.Contains(stdout, sequencedOperationID) || !strings.Contains(stdout, cliOperationID) {
			t.Fatal("operation list lost the original handles", err)
		}
		if output == "table" || output == "wide" {
			if !strings.Contains(stderr, "Next cursor: next-page") || strings.Contains(stdout, "Next cursor:") {
				t.Fatal("operation continuation polluted row output or disappeared")
			}
			for _, line := range strings.Split(stdout, "\n") {
				fields := strings.Fields(line)
				if len(fields) != 6 {
					continue
				}
				if fields[0] == sequencedOperationID && strings.Join(fields[2:], ",") != "unavailable,unavailable,unavailable,unavailable" {
					t.Fatal("missing operation observations were fabricated", line)
				}
				if fields[0] == cliOperationID && strings.Join(fields[2:], ",") != "0,0,0,no" {
					t.Fatal("explicit zero/false observations were lost", line)
				}
			}
			continue
		}
		var page map[string]any
		if output == "json" {
			err = json.Unmarshal([]byte(stdout), &page)
		} else {
			err = yaml.Unmarshal([]byte(stdout), &page)
		}
		if err != nil || page["nextCursor"] != "next-page" || page["snapshot"] != "snapshot-identity" || stderr != "" {
			t.Fatal("operation list is not a standalone canonical page", err)
		}
		items, ok := page["items"].([]any)
		if !ok || len(items) != 2 {
			t.Fatal("operation page shape changed")
		}
		for _, field := range []string{"uploaded", "committed", "applied", "validated"} {
			if _, exists := items[0].(map[string]any)[field]; exists {
				t.Fatal("omitted observation became a fabricated field", field)
			}
			if value, exists := items[1].(map[string]any)[field]; !exists || value == nil {
				t.Fatal("explicit zero/false observation was omitted", field)
			}
		}
		action := items[1].(map[string]any)["items"].([]any)[0].(map[string]any)
		if action["committed"] != false || action["applied"] != false {
			t.Fatal("explicit false item observations were lost")
		}
	}
	if requests.Load() != 4 {
		t.Fatal("operation list fetched additional pages or repeated requests")
	}
}

func TestManagementOperationsRejectUnsupportedFlagsBeforeRequests(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	for _, args := range [][]string{
		{"get", "operations", "--limit", "0"}, {"get", "operations", "--limit", "501"},
		{"get", "operations", "--all"}, {"get", "operations", "--selector", "team=infra"},
		{"get", "operations", "--monitor-id", ""}, {"get", "operations", "--monitor-id", "service/api"},
		{"get", "operations", sequencedOperationID, "--limit", "100"}, {"get", "operation", sequencedOperationID, "--cursor", "ignored"},
		{"get", "operations", sequencedOperationID, "--monitor-id", "service-api"},
	} {
		if stdout, _, err := fixture.run(t, nil, args...); err == nil || stdout != "" {
			t.Fatal("unsupported or ignored operation arguments were accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid operation list made a request")
	}
}

func TestManagementOperationsPropagateDeniedExpiredAndInvalidResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"expired", 410, `{"status":410,"code":"operationExpired","detail":"private-detail"}`, cpra.ErrExpired},
		{"denied", 403, `{"status":403,"detail":"private-detail"}`, cpra.ErrUnauthorized},
		{"null-count", 200, `{"items":[{"id":"` + sequencedOperationID + `","state":"reserved","contentDigest":"digest","committed":null}]}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if test.status != 200 {
					w.Header().Set("Content-Type", "application/problem+json")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			stdout, stderr, err := fixture.run(t, nil, "get", "operations", "--limit", "1", "--cursor", "expired")
			if err == nil || test.want != nil && !errors.Is(err, test.want) || requests.Load() != 1 || stdout != "" || strings.Contains(stderr+err.Error(), "private-detail") {
				t.Fatal("operation error retried, fell back, or exposed private details", err)
			}
		})
	}
}
