package cpra

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestControlMethodsUseStrongETagAndOpaqueBodyRevision(t *testing.T) {
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Match") != `"control-version"` || r.Header.Get("If-None-Match") != "" {
			t.Errorf("invalid conditional control headers: %q", r.Header.Get("If-Match"))
		}
		var request api.ControlRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Revision != "control-version" {
			t.Errorf("body revision must remain opaque: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/incidents/"):
			_ = json.NewEncoder(w).Encode(api.Incident{ID: "incident", MonitorID: "monitor", Revision: "next", State: "open"})
		case strings.HasPrefix(r.URL.Path, "/api/v2/actions/"):
			_ = json.NewEncoder(w).Encode(api.Action{ID: "action", State: "unknown"})
		default:
			_ = json.NewEncoder(w).Encode(api.Operation{ID: "operation", ContentDigest: strings.Repeat("a", 64), State: "committed"})
		}
	}, nil)
	request := api.ControlRequest{Revision: "control-version", Reason: "Planned maintenance", Duration: "1m", IncidentID: "incident", Resolution: "unknown"}
	ctx := context.Background()
	for name, invoke := range map[string]func() error{
		"acknowledge": func() error { _, err := client.Incidents.Acknowledge(ctx, "incident", request); return err },
		"dismiss":     func() error { _, err := client.Incidents.Dismiss(ctx, "incident", request); return err },
		"reopen":      func() error { _, err := client.Incidents.Reopen(ctx, "incident", request); return err },
		"snooze":      func() error { _, err := client.Monitors.Snooze(ctx, "monitor", request); return err },
		"unsnooze":    func() error { _, err := client.Monitors.Unsnooze(ctx, "monitor", request); return err },
		"recover":     func() error { _, err := client.Monitors.Recover(ctx, "monitor", request); return err },
		"review":      func() error { _, err := client.Actions.Review(ctx, "action", request); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
