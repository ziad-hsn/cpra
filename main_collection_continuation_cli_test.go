package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Interrupt the native client after the real server has admitted a phase but
// before its reply is delivered. Resume verifies input before activation; once
// activation was admitted, observation/wait uses its original operation handle.
func TestMainCollectionCLIContinuationAfterLostPhaseReply(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to execute the native CLI")
	}
	binary, binaryHash := buildMainCLI(t)
	for _, scenario := range []struct {
		name, phase string
		restart     bool
		finalize    bool
	}{
		{name: "upload-owner-restart", phase: "items", restart: true},
		{name: "validation-client-restart", phase: "validate"},
		{name: "validation-result-owner-restart", phase: "validate", restart: true, finalize: true},
		{name: "validation-owner-restart", phase: "validate", restart: true},
		{name: "activation-owner-restart", phase: "activate", restart: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			phase := scenario.phase
			fixture := newMainManagementFixture(t)
			cli := newCollectionRecoveryCLI(t, fixture, binary)
			var providerCalls atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { providerCalls.Add(1); w.WriteHeader(http.StatusOK) }))
			defer target.Close()
			source := "monitors:\n"
			for n := range 35 {
				source += fmt.Sprintf("  - id: continuation-%02d\n    name: Continuation %d\n    enabled: false\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 1s\n      unhealthy_threshold: 1\n      healthy_threshold: 1\n      config: {url: %s}\n", n, n, target.URL)
			}
			path := filepath.Join(filepath.Dir(fixture.options.runtimeFile), "monitors-to-apply.yaml")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			client, stop := startMainManagement(t, fixture)
			type admittedReply struct {
				id     string
				status int
			}
			admitted := make(chan admittedReply, 1)
			var intercepted atomic.Bool
			var mu sync.Mutex
			writes := map[string]int{}
			proxy := collectionCLIRecoveryProxy(t, fixture, func(r *http.Request) {
				if r.Method == http.MethodPost || r.Method == http.MethodPut {
					mu.Lock()
					writes[r.URL.Path]++
					mu.Unlock()
				}
			}, func(r *http.Response) error {
				if !strings.HasSuffix(r.Request.URL.Path, "/"+phase) || (r.Request.Method != http.MethodPost && r.Request.Method != http.MethodPut) || !intercepted.CompareAndSwap(false, true) {
					return nil
				}
				defer r.Body.Close()
				id := strings.TrimSuffix(strings.TrimPrefix(r.Request.URL.Path, "/api/v2/operations/"), "/"+phase)
				admitted <- admittedReply{id: id, status: r.StatusCode}
				<-r.Request.Context().Done()
				return errCLIRecoveryLostReply
			})
			cli.origin = proxy.URL
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			command := collectionRecoveryCommand(ctx, cli, "apply", "-f", path, "-o", "json")
			privateTemp := t.TempDir()
			command.Env = append(os.Environ(), "TMPDIR="+privateTemp, "TMP="+privateTemp, "TEMP="+privateTemp)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			exited := false
			t.Cleanup(func() {
				if !exited {
					_ = command.Process.Kill()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("interrupted CLI did not exit")
					}
				}
			})
			var original admittedReply
			select {
			case original = <-admitted:
			case err := <-done:
				exited = true
				t.Fatalf("CLI ended before selected admission: %v", err)
			case <-ctx.Done():
				t.Fatal("CLI did not reach selected admission")
			}
			if original.status != http.StatusOK && original.status != http.StatusAccepted {
				t.Fatalf("phase rejected before interruption: %d", original.status)
			}
			// A delayed client response must not change the controller's healthy state.
			state, err := client.State(t.Context())
			if err != nil || !state.Data.Ready || !state.Data.ControllerReady || !state.Data.Storage.Available {
				t.Fatal("phase admission poisoned readiness", err)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				exited = true
				if err == nil {
					t.Fatal("forced client interruption succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client did not stop after kill")
			}
			if strings.Contains(stdout.String()+stderr.String(), startupOperatorToken) {
				t.Fatal("interrupted output disclosed authentication")
			}
			before, err := client.Operations.Get(t.Context(), original.id)
			if err != nil || before.Data.ID != original.id || before.Data.Uploaded == nil || *before.Data.Uploaded != 35 {
				t.Fatal("original progress lost after reply loss", err)
			}
			if phase != "activate" {
				monitors, err := client.Monitors.List(t.Context(), cpra.ListOptions{Limit: 1})
				if err != nil || len(monitors.Data.Items) != 0 {
					t.Fatal("inactive phase applied resources", err)
				}
			}
			mu.Lock()
			phaseWrites := writes["/api/v2/operations/"+original.id+"/"+phase]
			mu.Unlock()
			if phaseWrites != 1 {
				t.Fatal("ordinary CLI retried mutation", phaseWrites)
			}
			proxy.Close()
			if scenario.finalize {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				validation, err := client.Operations.WaitValidation(ctx, original.id)
				if err != nil || !validation.Data.Summary.Valid {
					t.Fatal("original validation did not publish before restart", err)
				}
			}
			if scenario.restart {
				stop()
				client, stop = startMainManagement(t, fixture)
			}
			restored, err := client.Operations.Get(t.Context(), original.id)
			if err != nil {
				t.Fatal("cannot observe original collection after owner restart", err)
			}
			t.Logf("phase=%s original_state_before_stop=%s original_state_after_restart=%s", phase, before.Data.State, restored.Data.State)
			var continuationWrites atomic.Int64
			continuation := collectionCLIRecoveryProxy(t, fixture, func(r *http.Request) {
				if r.Method != http.MethodGet {
					continuationWrites.Add(1)
				}
			}, nil)
			cli.origin = continuation.URL
			cli.processTimeout = 45 * time.Second
			var completed api.Operation
			interrupted := restored.Data.State == "interrupted"
			if interrupted {
				if phase != "validate" || !scenario.restart || scenario.finalize {
					t.Fatal("an eligible original operation was interrupted")
				}
				result := cli.invoke(t, nil, "resume", "operation/"+original.id, "-f", path, "--wait", "--timeout=30s", "-o", "json")
				completed = decodeMainCLI[api.Operation](t, result.stdout)
				if result.err == nil || !strings.Contains(result.stderr, "validation was interrupted") || !strings.Contains(result.stderr, original.id) || continuationWrites.Load() != 0 {
					t.Fatal("interrupted validation was hidden or resubmitted", result.err, result.stderr)
				}
				_, err := client.Operations.Validation(t.Context(), original.id, cpra.ValidationPageOptions{})
				var problem *cpra.Error
				if !errors.As(err, &problem) || problem.Problem.Code != "validationInterrupted" {
					t.Fatal("original interruption evidence unavailable", err)
				}
			} else if phase == "activate" {
				completed = decodeMainCLI[api.Operation](t, cli.success(t, nil, "wait", "operation/"+original.id, "--timeout=30s", "-o", "json").stdout)
			} else {
				completed = decodeMainCLI[api.Operation](t, cli.success(t, nil, "resume", "operation/"+original.id, "-f", path, "--wait", "--timeout=30s", "-o", "json").stdout)
			}
			if completed.ID != original.id || completed.ContentDigest != before.Data.ContentDigest {
				t.Fatal("continuation lost original collection identity")
			}
			if interrupted {
				monitors, err := client.Monitors.List(t.Context(), cpra.ListOptions{Limit: 1})
				if err != nil || len(monitors.Data.Items) != 0 || completed.State != "interrupted" || completed.ExecutionResult != nil || completed.Committed == nil || *completed.Committed != 0 || completed.Applied == nil || *completed.Applied != 0 {
					t.Fatal("interrupted validation applied resources", err)
				}
			} else {
				if completed.ExecutionResult == nil || completed.ExecutionResult.State != "ready" || completed.ExecutionResult.Summary == nil || completed.ExecutionResult.Summary.Outcome != "completed" || completed.ExecutionResult.Summary.Accepted != 35 || completed.ExecutionResult.Summary.ChildApplied != 35 || len(completed.Items) != 35 {
					t.Fatal("continuation did not complete original collection exactly once")
				}
				for n := range 35 {
					resource, err := client.Monitors.Get(t.Context(), fmt.Sprintf("continuation-%02d", n))
					if err != nil || resource.Data.Metadata.Generation != 1 || resource.Data.Status.ObservedGeneration != 1 {
						t.Fatal("continuation duplicated mutation or lost projection", err)
					}
				}
			}
			operations, err := client.Operations.List(t.Context(), cpra.ListOptions{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			collections := 0
			for _, operation := range operations.Data.Items {
				if operation.ItemCount != nil && *operation.ItemCount == 35 {
					collections++
					if operation.ID != original.id {
						t.Fatal("replacement collection created")
					}
				}
			}
			if collections != 1 {
				t.Fatal("original collection inventory changed", collections)
			}
			stop()
			if providerCalls.Load() != 0 {
				t.Fatal("disabled resources caused provider operations")
			}
			t.Logf("phase=%s native_cli_sha256=%s forced_client_kill=true admitted_writes=1 owner_restarted=%t validation_finalized_before_restart=%t original_items=35 outcome=%s provider_calls=0", phase, binaryHash, scenario.restart, scenario.finalize, completed.State)
		})
	}
}
