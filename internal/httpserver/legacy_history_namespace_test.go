package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestLegacyHistoryRejectsCollectionNamespaces(t *testing.T) {
	f := newManagementFixture(t, true)
	op, _ := collectionCancellationHTTPSetup(t, f, true)
	if _, err := f.sdk.Operations.Cancel(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	page, err := f.server.cfg.Store.History().Page("collection/"+op.ID, "", 100)
	if err != nil || len(page.Events) != 1 || page.Events[0].Collection == nil || page.Events[0].Collection.ID != op.ID {
		t.Fatalf("missing internal collection receipt: %+v %v", page, err)
	}
	response, _ := f.request(t, http.MethodGet, "/api/v2/operations/"+op.ID, managementReaderToken, nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("reader can observe another actor's collection", response.StatusCode)
	}
	for _, prefix := range []string{"collection/", "collection-activation/", "collection-validation/", "collection-execution/"} {
		t.Run(strings.TrimSuffix(prefix, "/"), func(t *testing.T) {
			for _, suffix := range []string{"", op.ID} {
				for _, principal := range []struct{ name, token string }{
					{"owner", managementOperatorToken}, {"reader", managementReaderToken}, {"legacy", managementLegacyToken},
				} {
					path := "/api/v1/history?monitor_id=" + url.QueryEscape(prefix+suffix)
					response, body := f.request(t, http.MethodGet, path, principal.token, nil, nil)
					if response.StatusCode != http.StatusBadRequest || strings.TrimSpace(string(body)) != `{"error":"invalid history request"}` {
						t.Fatalf("%s history boundary: %d %s", principal.name, response.StatusCode, body)
					}
				}
			}
			for _, token := range []string{managementOperatorToken, managementReaderToken} {
				response, _ := f.request(t, http.MethodGet, "/api/v2/history?monitorID="+url.QueryEscape(prefix+op.ID), token, nil, nil)
				if response.StatusCode != http.StatusUnprocessableEntity {
					t.Fatal("v2 monitor history accepted a collection namespace", response.StatusCode)
				}
			}
		})
	}
}

func TestLegacyHistoryRejectsCollectionNamespacesBeforeStorageAccess(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AuthToken = managementLegacyToken
	handler := s.authMiddleware(apiMux(s))
	for _, monitorID := range []string{"collection/private", "collection-activation/private", "collection-validation/private", "collection-execution/private", "ordinary/monitor"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/history?monitor_id="+url.QueryEscape(monitorID), nil)
		request.Header.Set("Authorization", "Bearer "+managementLegacyToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		status, body := http.StatusBadRequest, `{"error":"invalid history request"}`
		if monitorID == "ordinary/monitor" {
			status, body = http.StatusServiceUnavailable, `{"error":"event history unavailable"}`
		}
		if response.Code != status || strings.TrimSpace(response.Body.String()) != body {
			t.Fatal(monitorID, response.Code, response.Body.String())
		}
	}
}

func TestLegacyHistoryPreservesNonreservedMonitorIDs(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, monitorID := range []string{"service", "team/service", "collection", "collection-activation", "collection-validation-monitor", "collection-execution-monitor", "prefix/collection-execution/op", "Collection-execution/op"} {
		t.Run(monitorID, func(t *testing.T) {
			monitor := persistence.Monitor{ID: monitorID, Revision: "r1", Name: "legacy monitor", Policy: persistence.Policy{Interval: time.Minute, Enabled: true, Unhealthy: 1, Healthy: 1}}
			at := time.Now().UTC()
			results, err := f.server.cfg.Store.Submit(t.Context(), []persistence.Command{
				{Kind: "configure", MonitorID: monitorID, Revision: monitor.Revision, Config: &monitor, At: at},
				{Kind: "pulse", MonitorID: monitorID, Revision: monitor.Revision, Generation: 1, Outcome: "failure", At: at},
				{Kind: "pulse", MonitorID: monitorID, Revision: monitor.Revision, Generation: 2, Outcome: "success", At: at},
			})
			if err != nil || len(results) != 3 {
				t.Fatal("legacy monitor history setup failed", err)
			}
			for _, result := range results {
				if result.Err != nil {
					t.Fatal(result.Err)
				}
			}
			stored, err := f.server.cfg.Store.History().Page(monitorID, "", 100)
			if err != nil || len(stored.Events) < 2 {
				t.Fatal("legacy monitor has no pageable history", err)
			}
			cursor := ""
			for _, expected := range stored.Events {
				query := url.Values{"monitor_id": {monitorID}, "limit": {"1"}, "cursor": {cursor}}
				response, body := f.request(t, http.MethodGet, "/api/v1/history?"+query.Encode(), managementLegacyToken, nil, nil)
				var page persistence.HistoryPage
				if response.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil || len(page.Events) != 1 || page.Events[0].ID != expected.ID || page.Events[0].MonitorID != monitorID {
					t.Fatalf("legacy monitor history changed: %d %s", response.StatusCode, body)
				}
				cursor = page.NextCursor
			}
			if cursor != "" {
				t.Fatal("unexpected continuation after final legacy event")
			}
		})
	}
}
