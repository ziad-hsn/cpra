package cpra

import (
	"context"
	"net/http"
	"testing"
)

func TestHistoryRequiresMonitorBeforeSending(t *testing.T) {
	calls := 0
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("monitorID") != "service" {
			t.Error("monitor ID missing from history request")
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}, nil)
	for _, id := range []string{"", "../service", "service?token=hidden"} {
		if _, err := client.History(context.Background(), ListOptions{MonitorID: id}); err == nil {
			t.Fatal("invalid monitor ID accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid history input sent a request")
	}
	if _, err := client.History(context.Background(), ListOptions{MonitorID: "service"}); err != nil {
		t.Fatal(err)
	}
}
