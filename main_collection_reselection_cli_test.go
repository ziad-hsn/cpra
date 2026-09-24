package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

var errCLIRecoveryLostReply = errors.New("fixture discarded committed response")

type collectionRecoveryCLIReport struct {
	OperationID string                            `json:"operationID"`
	AttemptID   string                            `json:"attemptID"`
	Complete    bool                              `json:"complete"`
	Operation   *api.Operation                    `json:"operation"`
	Attempt     *api.CollectionReselectionAttempt `json:"attempt"`
}

// The proxy forwards to the normal application's real TLS endpoint. It can
// discard or delay a response only after that endpoint has returned it.
func collectionCLIRecoveryProxy(t *testing.T, f mainManagementFixture, observe func(*http.Request), response func(*http.Response) error) *httptest.Server {
	t.Helper()
	target, err := url.Parse("https://" + f.options.webAddr)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	direct := proxy.Director
	proxy.Director = func(r *http.Request) { direct(r); r.Host = target.Host }
	transport := f.client.Transport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	proxy.Transport = transport
	proxy.ModifyResponse = response
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		if hijacker, ok := w.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observe != nil {
			observe(r)
		}
		proxy.ServeHTTP(w, r)
	}))
	certificate, err := tls.LoadX509KeyPair(f.settings.Management.TLS.CertFile, f.settings.Management.TLS.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func newCollectionRecoveryCLI(t *testing.T, f mainManagementFixture, binary string) *mainCLIRunner {
	t.Helper()
	token := filepath.Join(filepath.Dir(f.options.runtimeFile), "recovery-operator.token")
	if err := os.WriteFile(token, []byte(startupOperatorToken), 0600); err != nil {
		t.Fatal(err)
	}
	return &mainCLIRunner{binary: binary, origin: "https://" + f.options.webAddr, ca: f.settings.Management.TLS.CertFile, token: token, secret: "PRIVATE-NATIVE-RECOVERY"}
}

func collectionRecoveryCommand(ctx context.Context, cli *mainCLIRunner, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, cli.binary, append([]string{"--server", cli.origin, "--ca-file", cli.ca, "--token-file", cli.token, "--request-timeout", "15s"}, args...)...)
}

func runCollectionRecoveryCLI(t *testing.T, cli *mainCLIRunner, args ...string) mainCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := collectionRecoveryCommand(ctx, cli, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := mainCLIResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
	for _, secret := range []string{cli.secret, startupOperatorToken, startupReaderToken} {
		if strings.Contains(result.stdout, secret) || strings.Contains(result.stderr, secret) {
			t.Fatal("native recovery output disclosed private source data or authentication")
		}
	}
	return result
}

func requireInactiveCLIUpload(t *testing.T, client *cpra.Client, id string, uploaded, count int64) api.Operation {
	t.Helper()
	operation, err := client.Operations.Get(t.Context(), id)
	if err != nil || operation.Data.ID != id || operation.Data.NormalizationProfile != collection.FileNormalizationProfile || operation.Data.State != "uploading" || operation.Data.ItemCount == nil || *operation.Data.ItemCount != count || operation.Data.Uploaded == nil || *operation.Data.Uploaded != uploaded || operation.Data.Committed == nil || *operation.Data.Committed != 0 || operation.Data.Applied == nil || *operation.Data.Applied != 0 || operation.Data.ExecutionResult != nil {
		t.Fatal("original profiled operation identity/progress changed or active work was admitted", err)
	}
	credentials, err := client.Credentials.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(credentials.Data.Items) != 0 {
		t.Fatal("upload recovery activated a resource", err)
	}
	return operation.Data
}

func TestMainManagementCollectionReselectionCLI(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to build and execute the real CLI")
	}
	f := newMainManagementFixture(t)
	binary, binaryHash := buildMainCLI(t)
	cli := newCollectionRecoveryCLI(t, f, binary)
	private := filepath.Dir(f.options.runtimeFile)
	var source strings.Builder
	source.WriteString("# original raw file commitment\n")
	for n := 0; n < 300; n++ {
		fmt.Fprintf(&source, "---\napiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: native-%03d\nspec:\n  value: %s\n", n, cli.secret)
	}
	first, last := filepath.Join(private, "first.yaml"), filepath.Join(private, "empty.yaml")
	if err := os.WriteFile(first, []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(last, nil, 0600); err != nil {
		t.Fatal(err)
	}
	client, stop := startMainManagement(t, f)
	type uploadResponse struct {
		id       string
		status   int
		duration time.Duration
	}
	committed := make(chan uploadResponse, 1)
	var intercepted atomic.Bool
	var uploadStarted atomic.Int64
	proxy := collectionCLIRecoveryProxy(t, f, func(request *http.Request) {
		if request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/items") {
			uploadStarted.CompareAndSwap(0, time.Now().UnixNano())
		}
	}, func(response *http.Response) error {
		if response.Request.Method != http.MethodPut || !strings.HasSuffix(response.Request.URL.Path, "/items") || !intercepted.CompareAndSwap(false, true) {
			return nil
		}
		defer response.Body.Close()
		id := strings.TrimSuffix(strings.TrimPrefix(response.Request.URL.Path, "/api/v2/operations/"), "/items")
		committed <- uploadResponse{id: id, status: response.StatusCode, duration: time.Since(time.Unix(0, uploadStarted.Load()))}
		if response.StatusCode != http.StatusOK {
			return errCLIRecoveryLostReply
		}
		<-response.Request.Context().Done()
		return errCLIRecoveryLostReply
	})
	cli.origin = proxy.URL
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	command := collectionRecoveryCommand(ctx, cli, "apply", "--file-profile", collection.FileNormalizationProfile, "-f", first, "-f", last, "-o", "json")
	// A forced kill cannot run library cleanup; isolate that process's temporary
	// plaintext staging inside the test-owned directory instead of global /tmp.
	command.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = command.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("interrupted CLI did not exit")
		}
	})
	var id string
	var uploadDuration time.Duration
	select {
	case response := <-committed:
		id, uploadDuration = response.id, response.duration
		if response.status != http.StatusOK {
			operation, err := client.Operations.Get(t.Context(), id)
			var uploaded int64 = -1
			if err == nil && operation.Data.Uploaded != nil {
				uploaded = *operation.Data.Uploaded
			}
			t.Fatalf("first upload response status=%d committed_resources=%d", response.status, uploaded)
		}
	case <-done:
		t.Fatalf("CLI stopped before upload was committed: %v; %s", waitErr, stderr.String())
	case <-ctx.Done():
		t.Fatal("CLI did not reach committed partial upload")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	if waitErr == nil {
		t.Fatal("forced CLI termination unexpectedly succeeded")
	}
	for _, secret := range []string{cli.secret, startupOperatorToken} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatal("interrupted CLI disclosed private input")
		}
	}
	before := requireInactiveCLIUpload(t, client, id, 256, 300)
	proxy.Close()
	stop()
	store, err := persistence.Open(t.Context(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := store.CollectionPage(id, 0, 256)
	if err != nil || len(prefix) != 256 {
		t.Fatal("committed original prefix unavailable", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	f.options.webAddr = availableLocalAddress(t)
	client, stop = startMainManagement(t, f)
	cli.origin = "https://" + f.options.webAddr
	if restored := requireInactiveCLIUpload(t, client, id, 256, 300); restored.ContentDigest != before.ContentDigest {
		t.Fatal("restart changed original inventory")
	}
	// Even a change to an empty final source must prevent suffix transfer.
	if err := os.WriteFile(last, []byte("# changed after client restart\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := runCollectionRecoveryCLI(t, cli, "resume-upload", "operation/"+id, "-f", first, "-f", last, "-o", "json")
	if bad.err == nil || !strings.Contains(bad.stdout+bad.stderr, id) {
		t.Fatal("changed final source did not reject recovery with original handle")
	}
	requireInactiveCLIUpload(t, client, id, 256, 300)
	stop()
	// Restart deliberately discards the failed disposable attempt; it does not
	// cancel/recreate the committed original collection.
	f.options.webAddr = availableLocalAddress(t)
	client, stop = startMainManagement(t, f)
	cli.origin = "https://" + f.options.webAddr
	if err := os.WriteFile(last, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := runCollectionRecoveryCLI(t, cli, "resume-upload", "operation/"+id, "-f", first, "-f", last, "-o", "json")
	if result.err != nil {
		t.Fatalf("original native upload recovery failed: %v; %s", result.err, result.stderr)
	}
	if !json.Valid([]byte(result.stdout)) || !strings.Contains(result.stdout, id) {
		t.Fatal("native recovery did not return bounded original progress")
	}
	report := decodeMainCLI[collectionRecoveryCLIReport](t, result.stdout)
	if !report.Complete || report.OperationID != id || report.AttemptID == "" || report.Operation == nil || report.Operation.Uploaded == nil || *report.Operation.Uploaded != 300 {
		t.Fatal("native recovery report did not confirm the original complete upload")
	}
	after := requireInactiveCLIUpload(t, client, id, 300, 300)
	if after.ContentDigest != before.ContentDigest {
		t.Fatal("recovery replaced original content identity")
	}
	stop()
	store, err = persistence.Open(t.Context(), f.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.CollectionPage(id, 0, 256)
	if err != nil || !reflect.DeepEqual(prefix, restored) {
		t.Fatal("native recovery rewrote accepted ciphertext", err)
	}
	scanMainCLIPrivateState(t, f.settings.Storage.Directory, cli.secret)
	t.Logf("native CLI upload recovery: binary_sha256=%s forced_client_kill=true original_resources=300 preserved_prefix=256 initial_upload_duration=%s changed_final_source=rejected activated_resources=0", binaryHash, uploadDuration)
}

func TestMainManagementCollectionReselectionCLILostReplies(t *testing.T) {
	if os.Getenv("CPRA_RUN_CLI_INTEGRATION") != "1" {
		t.Skip("set CPRA_RUN_CLI_INTEGRATION=1 to build and execute the real CLI")
	}
	f := newMainManagementFixture(t)
	binary, binaryHash := buildMainCLI(t)
	cli := newCollectionRecoveryCLI(t, f, binary)
	cli.secret = "PRIVATE-RESELECTION-MAIN-"
	client, stop := startMainManagement(t, f)
	id, raw := seedMainCollectionReselection(t, client)
	var paths []string
	for n, data := range raw {
		path := filepath.Join(filepath.Dir(f.options.runtimeFile), fmt.Sprintf("source-%d.yaml", n))
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	var mu sync.Mutex
	writes := make(map[string]int)
	var sourceLost, resumeLost atomic.Bool
	attempt := make(chan string, 1)
	proxy := collectionCLIRecoveryProxy(t, f, func(r *http.Request) {
		if r.Method != http.MethodGet {
			mu.Lock()
			writes[r.Method+" "+r.URL.Path]++
			mu.Unlock()
		}
	}, func(response *http.Response) error {
		path := response.Request.URL.Path
		if response.Request.Method == http.MethodPut && strings.Contains(path, "/reselection/") && sourceLost.CompareAndSwap(false, true) {
			parts := strings.Split(strings.Trim(path, "/"), "/")
			if len(parts) == 8 && response.StatusCode == http.StatusOK {
				attempt <- parts[5]
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			return errCLIRecoveryLostReply
		}
		if response.Request.Method == http.MethodPost && strings.HasSuffix(path, "/resume") && resumeLost.CompareAndSwap(false, true) {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			return errCLIRecoveryLostReply
		}
		return nil
	})
	cli.origin = proxy.URL
	args := []string{"resume-upload", "operation/" + id, "-o", "json"}
	for _, path := range paths {
		args = append(args, "-f", path)
	}
	first := runCollectionRecoveryCLI(t, cli, args...)
	if first.err == nil {
		t.Fatal("lost real source response was treated as confirmed")
	}
	var attemptID string
	select {
	case attemptID = <-attempt:
	default:
		t.Fatal("source request did not reach real server")
	}
	if !strings.Contains(first.stdout+first.stderr, id) || !strings.Contains(first.stdout+first.stderr, attemptID) {
		t.Fatal("unconfirmed source upload lost original safe handles")
	}
	observed := runCollectionRecoveryCLI(t, cli, "get", "upload-attempt", id, attemptID, "-o", "json")
	if observed.err != nil || !json.Valid([]byte(observed.stdout)) {
		t.Fatal("native read-only attempt observation failed", observed.err)
	}
	read := decodeMainCLI[collectionRecoveryCLIReport](t, observed.stdout)
	if read.Complete || read.OperationID != id || read.AttemptID != attemptID || read.Attempt == nil || read.Attempt.Phase != "uploading" || read.Attempt.SourcesCompleted != 1 {
		t.Fatal("read-only native attempt progress disagrees with admitted source")
	}
	second := runCollectionRecoveryCLI(t, cli, append(args, "--attempt", attemptID)...)
	if second.err == nil || !resumeLost.Load() {
		t.Fatal("lost real resume response was silently accepted or never reached server")
	}
	waitMainReselection(t, client, id, attemptID, "completed")
	third := runCollectionRecoveryCLI(t, cli, "resume-upload", "operation/"+id, "--attempt", attemptID, "-o", "json")
	if third.err != nil {
		t.Fatal("new CLI process did not reconcile completed original upload", third.err)
	}
	final := decodeMainCLI[collectionRecoveryCLIReport](t, third.stdout)
	if !final.Complete || final.OperationID != id || final.Operation == nil || final.Operation.Uploaded == nil || *final.Operation.Uploaded != 2 {
		t.Fatal("completed recovery was not reported without another mutation")
	}
	requireInactiveCLIUpload(t, client, id, 2, 2)
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 6 {
		t.Fatal("recovery submitted unexpected mutation paths", writes)
	}
	for path, count := range writes {
		if count != 1 || !strings.HasPrefix(path[strings.IndexByte(path, ' ')+1:], "/api/v2/operations/"+id+"/reselection") {
			t.Fatal("lost replies repeated mutation or changed original operation", path, count)
		}
	}
	proxy.Close()
	stop()
	t.Logf("native CLI lost-response recovery: binary_sha256=%s processes=4 original_operation=true source_puts=3 verifies=1 resumes=1 new_collections=0 active_resources=0", binaryHash)
}
