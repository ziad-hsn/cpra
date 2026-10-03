package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestMainA2AFileProcess(t *testing.T) {
	file := os.Getenv("CPRA_A2A_FILE_RUNTIME")
	if file == "" {
		return
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	options := runOptions{runtimeFile: file, manifest: os.Getenv("CPRA_A2A_FILE_MANIFEST"),
		web: true, webAddr: "127.0.0.1:0", webAuth: startupReaderToken,
		allowEmpty: true, shutdownTimeout: 5 * time.Second}
	var address string
	options.webStarted = func(bound string) { address = bound }
	if err := runCPRa(ctx, options, func() {
		if address == "" {
			panic("file-only owner ready without a bound listener")
		}
		path := os.Getenv("CPRA_A2A_FILE_READY")
		if err := os.WriteFile(path+".tmp", []byte(address), 0600); err != nil {
			panic(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			panic(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
}

type agentFileProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	logPath string
}

func (p *agentFileProcess) stop(t *testing.T, force bool) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	if force {
		if err := p.command.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = p.command.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		_ = p.command.Process.Kill()
		<-p.done
		t.Fatal("file-only process missed shutdown deadline")
	}
	if !force && p.err != nil || force && p.err == nil {
		t.Fatal("unexpected process termination", p.err)
	}
	data, _ := os.ReadFile(p.logPath)
	if bytes.Contains(data, []byte("WARNING: DATA RACE")) {
		t.Fatal("file-only process reported a race")
	}
}

func startAgentFileProcess(t *testing.T, f *mainManagementFixture, expectReady bool) *agentFileProcess {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	logPath := filepath.Join(dir, "process.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMainA2AFileProcess$", "-test.timeout=45s")
	command.Env = append(os.Environ(), "CPRA_A2A_FILE_RUNTIME="+f.options.runtimeFile,
		"CPRA_A2A_FILE_MANIFEST="+f.options.manifest, "CPRA_A2A_FILE_READY="+ready)
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	p := &agentFileProcess{command: command, done: make(chan struct{}), logPath: logPath}
	go func() { p.err = command.Wait(); _ = log.Close(); close(p.done) }()
	t.Cleanup(func() { p.stop(t, true) })
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		if raw, err := os.ReadFile(ready); err == nil {
			if !expectReady {
				t.Fatal("invalid restart reached readiness")
			}
			address := string(raw)
			host, port, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
				t.Fatal("file-only owner did not report its bound loopback address", address, err)
			}
			f.options.webAddr = address
			return p
		} else if !os.IsNotExist(err) {
			t.Fatal("cannot read file-only owner readiness", err)
		}
		select {
		case <-p.done:
			if _, err := os.Stat(ready); !expectReady && err == nil {
				t.Fatal("invalid restart reported readiness before exiting")
			}
			if expectReady || p.err == nil {
				data, _ := os.ReadFile(logPath)
				t.Fatalf("unexpected startup result: %v\n%s", p.err, data)
			}
			return p
		case <-deadline.C:
			t.Fatal("file-only process did not finish initialization")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func fileAgentAction(t *testing.T, f *mainManagementFixture, state string) persistence.Action {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+f.options.webAddr+"/api/v1/state?monitor_id=agent-monitor", nil)
		req.Header.Set("Authorization", "Bearer "+startupReaderToken)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("read file-only action", err)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("file-only action request failed", response.StatusCode, err)
		}
		if bytes.Contains(raw, []byte("agent_connection")) || bytes.Contains(raw, []byte("file-agent-private-token")) {
			t.Fatal("read-only state exposed its internal connection")
		}
		var body struct {
			Actions []persistence.Action `json:"actions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		for _, action := range body.Actions {
			if action.State == persistence.Unknown && action.AgentReceipt != nil && action.AgentReceipt.State == state {
				return action
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("file-only action did not reach the expected committed observation")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestMainA2AFileRestartPreservesOriginalConnection(t *testing.T) {
	for _, role := range []string{"intervention", "notification"} {
		for _, change := range []string{"unchanged", "reconfigured", "removed"} {
			t.Run(role+"/"+change, func(t *testing.T) {
				f := newMainManagementFixture(t)
				f.options.manifest = filepath.Join(filepath.Dir(f.options.manifest), "monitors.json")
				f.settings.Storage.Encryption = f.settings.Management.Encryption
				f.settings.Management = runtimeconfig.Management{}
				writeMainRuntime(t, f.options.runtimeFile, f.settings)
				var sends, reads, replacementCalls atomic.Int32
				var complete atomic.Bool
				agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer file-agent-private-token" || r.Header.Get("A2A-Version") != "1.0" {
						t.Error("original agent credentials were not preserved")
					}
					state := "TASK_STATE_INPUT_REQUIRED"
					if complete.Load() {
						state = "TASK_STATE_COMPLETED"
					}
					task := `{"id":"file-task","contextId":"file-context","status":{"state":"` + state + `"}}`
					w.Header().Set("Content-Type", "application/a2a+json")
					switch {
					case r.Method == "POST" && r.URL.Path == "/message:send":
						sends.Add(1)
						_, _ = w.Write([]byte(`{"task":` + task + `}`))
					case r.Method == "GET" && r.URL.Path == "/tasks/file-task" && r.URL.Query().Get("historyLength") == "0":
						reads.Add(1)
						_, _ = w.Write([]byte(task))
					default:
						t.Error("unexpected agent operation")
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer agent.Close()
				replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { replacementCalls.Add(1); w.WriteHeader(500) }))
				defer replacement.Close()
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
				defer target.Close()
				connection := map[string]any{"url": agent.URL, "token": "file-agent-private-token", "allow_insecure_http": true, "request_timeout": "2s"}
				monitor := map[string]any{"id": "agent-monitor", "name": "agent", "enabled": true,
					"pulse_check": map[string]any{"type": "http", "interval": "100ms", "timeout": "1s", "unhealthy_threshold": 1, "healthy_threshold": 1, "config": map[string]any{"url": target.URL}}}
				input := map[string]any{"monitors": []any{monitor}}
				if role == "intervention" {
					connection["task_timeout"] = "2s"
					monitor["intervention"] = map[string]any{"action": "a2a", "max_failures": 1, "target": connection}
				} else {
					connection["timeout"] = "2s"
					input["endpoints"] = map[string]any{"agent": map[string]any{"type": "a2a", "config": connection}}
					input["notification_groups"] = map[string]any{"agents": []string{"agent"}}
					monitor["codes"] = map[string]any{"red": map[string]any{"notify_group": "agents"}}
				}
				writeInput := func() {
					raw, _ := json.Marshal(input)
					if err := os.WriteFile(f.options.manifest, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				writeInput()
				process := startAgentFileProcess(t, f, true)
				original := fileAgentAction(t, f, "TASK_STATE_INPUT_REQUIRED")
				process.stop(t, true)
				if sends.Load() != 1 {
					t.Fatal("ambiguous action was resent")
				}
				store, err := persistence.Open(t.Context(), f.settings)
				if err != nil {
					t.Fatal(err)
				}
				record, found, err := store.Action(original.ID)
				if err != nil || !found || record.AgentConnection == nil || !record.Held {
					_ = store.Close()
					t.Fatal("forced restart lost the encrypted connection or hold", err)
				}
				keyring, err := encryptionsetup.Open(t.Context(), encryptionsetup.Options{StorageMode: "raft", DataDirectory: f.settings.Storage.Directory, Encryption: f.settings.EncryptionSource()})
				if err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				retained, err := management.OpenAgentConnection(t.Context(), keyring.Sealer(), store.Status().NodeID, record)
				_ = keyring.Close()
				if err != nil || retained.URL != agent.URL || retained.Token != "file-agent-private-token" {
					_ = store.Close()
					t.Fatal("original connection was not authenticated", err)
				}
				if change == "removed" {
					if err := store.Snapshot(); err != nil {
						_ = store.Close()
						t.Fatal("snapshot retained agent connection", err)
					}
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				monitor["enabled"] = false
				if change == "reconfigured" {
					connection["url"], connection["token"] = replacement.URL, "replacement-agent-token"
					previous := f.settings.Storage.Encryption.Local.ActiveKeyFile
					rotated := filepath.Join(filepath.Dir(f.options.runtimeFile), "rotated.key")
					if _, err := secureconfig.GenerateLocalKeyFile(t.Context(), secureconfig.KeyFileOptions{Path: rotated, DataDirectory: f.settings.Storage.Directory}); err != nil {
						t.Fatal(err)
					}
					f.settings.Storage.Encryption = &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: rotated, PreviousKeyFiles: []string{previous}}}
					writeMainRuntime(t, f.options.runtimeFile, f.settings)
				}
				if change == "removed" {
					input["monitors"] = []any{}
				}
				writeInput()
				if role == "intervention" && change == "removed" {
					// Retained actions still require their original key when the
					// current manifest no longer contains any A2A configuration.
					for _, failure := range []string{"omitted", "missing", "wrong"} {
						bad := f.settings
						switch failure {
						case "omitted":
							bad.Storage.Encryption = nil
						case "missing":
							bad.Storage.Encryption = &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: filepath.Join(filepath.Dir(f.options.runtimeFile), "absent.key")}}
						case "wrong":
							path := filepath.Join(filepath.Dir(f.options.runtimeFile), "wrong.key")
							if _, err := secureconfig.GenerateLocalKeyFile(t.Context(), secureconfig.KeyFileOptions{Path: path, DataDirectory: f.settings.Storage.Directory}); err != nil {
								t.Fatal(err)
							}
							bad.Storage.Encryption = &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: path}}
						}
						writeMainRuntime(t, f.options.runtimeFile, bad)
						before := reads.Load()
						failed := startAgentFileProcess(t, f, false)
						data, _ := os.ReadFile(failed.logPath)
						expected := "recover agent connections"
						if failure == "missing" {
							expected = "encryption backend initialization failed"
						}
						if !bytes.Contains(data, []byte(expected)) || bytes.Contains(data, []byte("WARNING: DATA RACE")) {
							t.Fatal("restart failed outside its expected key boundary", failure)
						}
						if reads.Load() != before || sends.Load() != 1 || bytes.Contains(data, []byte("file-agent-private-token")) {
							t.Fatal("failed key recovery invoked provider or exposed credentials")
						}
					}
					writeMainRuntime(t, f.options.runtimeFile, f.settings)
				}
				complete.Store(true)
				restarted := startAgentFileProcess(t, f, true)
				observed := fileAgentAction(t, f, "TASK_STATE_COMPLETED")
				if observed.ID != original.ID || observed.Revision != original.Revision || !observed.Held() || observed.Review != nil || sends.Load() != 1 || reads.Load() == 0 || replacementCalls.Load() != 0 {
					t.Fatal("restart repeated, redirected, lost or automatically resolved original work")
				}
				restarted.stop(t, false)
				for _, p := range []*agentFileProcess{process, restarted} {
					data, _ := os.ReadFile(p.logPath)
					if bytes.Contains(data, []byte("file-agent-private-token")) {
						t.Fatal("process log exposed agent credentials")
					}
				}
				if err := filepath.WalkDir(f.settings.Storage.Directory, func(path string, entry os.DirEntry, err error) error {
					if err != nil || entry.IsDir() {
						return err
					}
					data, err := os.ReadFile(path)
					if err == nil && bytes.Contains(data, []byte("file-agent-private-token")) {
						return fmt.Errorf("plaintext agent token in %s", filepath.Base(path))
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMainA2AFileValidationRequiresKeyBeforeStorage(t *testing.T) {
	f := newMainManagementFixture(t)
	f.settings.Management = runtimeconfig.Management{}
	writeMainRuntime(t, f.options.runtimeFile, f.settings)
	input := `monitors:
  - name: agent
    pulse_check: {type: http, interval: 1m, timeout: 1s, config: {url: https://service.example}}
    intervention: {action: a2a, target: {url: https://agent.example}}
`
	if err := os.WriteFile(f.options.manifest, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	for _, validate := range []bool{true, false} {
		f.options.validate = validate
		err := runCPRa(t.Context(), f.options, func() { t.Error("missing encryption reached readiness") })
		if err == nil || !strings.Contains(err.Error(), "storage.encryption") {
			t.Fatal("missing encryption was not rejected", err)
		}
		if _, err := os.Stat(f.settings.Storage.Directory); !os.IsNotExist(err) {
			t.Fatal("validation opened durable storage")
		}
	}
}
