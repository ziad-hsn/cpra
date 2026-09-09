//go:build mongo && kubernetes && postgres

package jobs

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpra/internal/loader/schema"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/dns"
)

func TestMongoSRVHonorsDeadline(t *testing.T) {
	previous := dns.DefaultResolver
	dns.DefaultResolver = &dns.Resolver{LookupTXT: func(string) ([]string, error) { time.Sleep(250 * time.Millisecond); return nil, nil }, LookupSRV: func(string, string, string) (string, []*net.SRV, error) {
		return "", nil, fmt.Errorf("fixture DNS failure")
	}}
	defer func() { dns.DefaultResolver = previous }()
	j := &PulseMongoJob{URI: "mongodb+srv://fixture.example.invalid", Timeout: 25 * time.Millisecond}
	start := time.Now()
	r := j.Execute()
	elapsed := time.Since(start)
	if elapsed > 150*time.Millisecond {
		t.Errorf("25ms operation blocked %v in SRV URI parsing: %v", elapsed, r.Err)
	}
}

func TestKubernetesExecAuthHonorsDeadline(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"fixture"}}`)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: fixture
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: fixture
  context:
    cluster: fixture
    user: fixture
current-context: fixture
users:
- name: fixture
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      interactiveMode: Never
      command: /bin/sh
      args:
      - -c
      - 'sleep 0.3; printf "{\"apiVersion\":\"client.authentication.k8s.io/v1\",\"kind\":\"ExecCredential\",\"status\":{\"token\":\"fixture\"}}"'
`, srv.URL)
	if err := os.WriteFile(p, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	j := &InterventionKubernetesJob{KubeconfigPath: p, Kind: "deployment", Namespace: "default", Name: "fixture", Timeout: 25 * time.Millisecond}
	start := time.Now()
	r := j.Execute()
	elapsed := time.Since(start)
	if elapsed > 150*time.Millisecond {
		t.Errorf("25ms recovery blocked %v in exec credential plugin: %v", elapsed, r.Err)
	}
}

func TestPostgresPasswordRedaction(t *testing.T) {
	valid := "host=127.0.0.1 user=fixture password='first\\' private-review-tail' dbname=fixture sslmode=disable"
	parsed, err := pgx.ParseConfig(valid)
	if err != nil || parsed.Password != "first' private-review-tail" {
		t.Fatalf("valid quoted password fixture failed: %v", err)
	}
	cfg := &schema.PulsePostgresConfig{DSN: strings.Replace(valid, "sslmode=disable", "sslmode=bad-mode", 1)}
	j := &PulsePostgresJob{ConnString: postgresConnString(cfg), Timeout: 25 * time.Millisecond}
	r := j.Execute()
	if r.Err == nil {
		t.Fatal("expected invalid sslmode")
	}
	if strings.Contains(r.Err.Error(), "private-review-tail") {
		t.Errorf("password suffix reaches result/log error: %v", r.Err)
	}
}
