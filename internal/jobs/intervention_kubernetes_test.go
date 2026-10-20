//go:build kubernetes

package jobs

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestKubernetesMutationDoesNotRetryServerError(t *testing.T) {
	for _, scale := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "scale"}[scale], func(t *testing.T) {
			var effects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method != http.MethodPatch && r.Method != http.MethodPut {
					t.Errorf("unexpected operation: %s", r.Method)
				}
				effects.Add(1)
				w.Header().Set("Retry-After", "1")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":500}`)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "kubeconfig")
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: %s\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\ncurrent-context: fixture\n", server.URL)
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			job := &InterventionKubernetesJob{KubeconfigPath: path, Namespace: "fixture", Name: "workload", Kind: "deployment", Timeout: 3 * time.Second}
			if scale {
				n := 2
				job.Replicas = &n
			}
			result := job.Execute()
			if effects.Load() != 1 || ClassifyEffect(result.Err).Outcome != EffectUnknown {
				t.Fatalf("mutation retried or effect made definite: calls=%d err=%v", effects.Load(), result.Err)
			}
		})
	}
}
