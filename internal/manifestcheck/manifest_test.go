package manifestcheck

import (
	"context"
	"fmt"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManifestValidationDoesNotContactTarget(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	contents := fmt.Sprintf("monitors:\n  - name: example\n    pulse_check:\n      type: http\n      interval: 1m\n      timeout: 5s\n      config: {url: %q}\n", target.URL)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Manifest(context.Background(), path, false, false, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("validation invoked provider")
	}
}

func TestFileNotificationRequiresProcessDirectory(t *testing.T) {
	for _, group := range []bool{false, true} {
		input := "monitors:\n  - name: fixture\n    pulse_check: {type: tcp, interval: 1m, timeout: 1s, config: {host: localhost, port: 80}}\n"
		if group {
			input += "    codes: {red: {notify_group: team}}\nendpoints: {log: {type: log, config: {file: alerts.jsonl}}}\nnotification_groups: {team: [log]}\n"
		} else {
			input += "    codes: {red: {notify: log, config: {file: alerts.jsonl}}}\n"
		}
		path := filepath.Join(t.TempDir(), "monitors.yaml")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Manifest(t.Context(), path, false, false, false); err == nil || !strings.Contains(err.Error(), "notifications.log_directory") {
			t.Fatal("unconfigured file output admitted", err)
		}
		if err := Manifest(t.Context(), path, false, false, true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDuplicateIdentityAcrossParserBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.json")
	var entries []string
	for i := 0; i < 1001; i++ {
		entries = append(entries, fmt.Sprintf(`{"id":"id-%d","name":"m-%d","pulse_check":{"type":"tcp","interval":"1m","timeout":"1s","config":{"host":"127.0.0.1","port":1}}}`, i%1000, i))
	}
	if err := os.WriteFile(path, []byte(`{"monitors":[`+strings.Join(entries, ",")+`]}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Manifest(ctx, path, false, false, false)
	field, ok := api.ValidationFieldError(err)
	if !ok || field.Reason != "duplicateIdentity" || field.Field != "id" || !strings.Contains(err.Error(), "monitor 1001 at line 1 column") {
		t.Fatalf("duplicate result: %v", err)
	}
}

func TestEmptyManifestRequiresOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	if err := os.WriteFile(path, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Manifest(context.Background(), path, false, false, false); err == nil {
		t.Fatal("empty accepted")
	}
	if err := Manifest(context.Background(), path, true, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestA2AFileRequiresEncryptionWithoutProviderRequests(t *testing.T) {
	for _, role := range []string{"intervention", "inline-notification", "group-notification"} {
		t.Run(role, func(t *testing.T) {
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer target.Close()
			connection := fmt.Sprintf("{url: %q, allow_insecure_http: true}", target.URL)
			input := "monitors:\n  - name: agent\n    pulse_check: {type: http, interval: 1m, timeout: 1s, config: {url: " + target.URL + "}}\n"
			switch role {
			case "intervention":
				input += "    intervention: {action: a2a, target: " + connection + "}\n"
			case "inline-notification":
				input += "    codes: {red: {notify: a2a, config: " + connection + "}}\n"
			case "group-notification":
				input += "    codes: {red: {notify_group: agents}}\nendpoints: {agent: {type: a2a, config: " + connection + "}}\nnotification_groups: {agents: [agent]}\n"
			}
			path := filepath.Join(t.TempDir(), "monitors.yaml")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Manifest(t.Context(), path, false, false, false); err == nil || !strings.Contains(err.Error(), "storage.encryption") {
				t.Fatal("missing key selection accepted", err)
			}
			if err := Manifest(t.Context(), path, false, true, false); err != nil {
				t.Fatal("explicit key selection rejected", err)
			}
			if calls.Load() != 0 {
				t.Fatal("preflight contacted a provider")
			}
		})
	}
}
