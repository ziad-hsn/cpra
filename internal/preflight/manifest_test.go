package preflight

import (
	"context"
	"fmt"
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
	if err := Manifest(context.Background(), path, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("validation invoked provider")
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
	if err := Manifest(ctx, path, false); err == nil || !strings.Contains(err.Error(), "duplicate effective monitor id") {
		t.Fatalf("duplicate result: %v", err)
	}
}

func TestEmptyManifestRequiresOptIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitors.yaml")
	if err := os.WriteFile(path, []byte("monitors: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Manifest(context.Background(), path, false); err == nil {
		t.Fatal("empty accepted")
	}
	if err := Manifest(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
}
