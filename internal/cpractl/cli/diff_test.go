package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"gopkg.in/yaml.v3"
)

func diffResponse(t *testing.T, r *http.Request) api.Preflight {
	t.Helper()
	if r.Method != "POST" || r.URL.Path != "/api/v2/collections/preflight" || r.Header.Get("Authorization") != "Bearer named-operator-token" {
		t.Error("diff used wrong route or identity")
	}
	var request api.PreflightRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
	}
	if request.IdentityFormat != commitment.Format || request.IdentityKey == nil || request.SourceFingerprint == nil || request.ItemCount != int64(len(request.Items)) {
		t.Error("diff did not use frozen inventory contract")
	}
	result := api.Preflight{Valid: true, IdentityFormat: commitment.Format, ContentDigest: request.ContentDigest, ItemCount: api.Pointer(request.ItemCount), Items: []api.ApplyResult{}}
	for _, item := range request.Items {
		result.Items = append(result.Items, api.ApplyResult{ID: item.ID, Outcome: "create", Committed: api.Pointer(false), Applied: api.Pointer(false)})
	}
	return result
}
func diffFile(t *testing.T, dir, name string, resources ...api.Resource) string {
	t.Helper()
	var content bytes.Buffer
	for _, resource := range resources {
		if err := json.NewEncoder(&content).Encode(resource); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func diffSpool(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}
func assertDiffSpoolRemoved(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "cpra-collection-*"))
	if err != nil || len(matches) != 0 {
		t.Fatal("private collection staging was retained")
	}
}

func TestDiffMultipleInputsFormatsAndExitStatus(t *testing.T) {
	spool := diffSpool(t)
	dir := t.TempDir()
	first := diffFile(t, dir, "one.json", cliResource("Monitor", "existing"))
	second := diffFile(t, dir, "two.json", cliResource("Credential", "private"), cliResource("Monitor", "new"))
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		result := diffResponse(t, r)
		result.Items[0].Outcome, result.Items[0].OldVersion = "update", "original-version"
		_ = json.NewEncoder(w).Encode(result)
	})
	for _, output := range []string{"json", "yaml", "table", "wide"} {
		stdout, stderr, err := fixture.run(t, nil, "diff", "-f", first, "-f", second, "-o", output)
		if !errors.Is(err, ErrDifferences) || ExitCode(err) != 1 || stderr != "" || strings.Contains(stdout, cliSecretValue) || strings.Contains(stdout, "contentDigest") || strings.Contains(stdout, "identityKey") || strings.Contains(stdout, "sourceFingerprint") {
			t.Fatal("diff failed or leaked private input", err)
		}
		if output == "json" || output == "yaml" {
			var result diffReport
			var decodeErr error
			if output == "json" {
				decodeErr = json.Unmarshal([]byte(stdout), &result)
			} else {
				decodeErr = yaml.Unmarshal([]byte(stdout), &result)
			}
			if decodeErr != nil || !result.Valid || !result.Changed || len(result.Items) != 3 || result.Items[0].OldVersion != "original-version" || result.Items[1].Source.File != second || result.Items[2].Source.Document != 2 || result.Items[2].Source.Item != 1 {
				t.Fatal("missing diff or local source attribution", decodeErr)
			}
		} else if !strings.Contains(stdout, "original-version") || !strings.Contains(stdout, second) {
			t.Fatal("table omitted useful diff observations")
		}
		assertDiffSpoolRemoved(t, spool)
	}
	if calls.Load() != 4 {
		t.Fatal("diff implicitly fetched, retried or activated")
	}
}

func TestDiffUnchangedEmptyAndLegacyExitCompatibility(t *testing.T) {
	spool := diffSpool(t)
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		result := diffResponse(t, r)
		for i := range result.Items {
			result.Items[i].Outcome = "unchanged"
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	raw, _ := json.Marshal(cliResource("Monitor", "existing"))
	for _, input := range [][]byte{raw, []byte("# empty\n")} {
		stdout, _, err := fixture.run(t, input, "diff", "-f", "-", "-o", "json")
		var result diffReport
		if err != nil || json.Unmarshal([]byte(stdout), &result) != nil || !result.Valid || result.Changed {
			t.Fatal("unchanged/empty diff was not a no-op", err)
		}
	}
	if calls.Load() != 1 || ExitCode(nil) != 0 || ExitCode(errors.New("ordinary command failure")) != 1 {
		t.Fatal("exit compatibility or empty-input behavior changed")
	}
	assertDiffSpoolRemoved(t, spool)
}

func TestDiffInvalidInputAndQuotaFinishBeforeAnyAPIRequest(t *testing.T) {
	spool := diffSpool(t)
	dir := t.TempDir()
	first := diffFile(t, dir, "one.json", cliResource("Credential", "private"))
	malformed := filepath.Join(dir, "last.yaml")
	if err := os.WriteFile(malformed, []byte("bad: ["+cliSecretValue), 0600); err != nil {
		t.Fatal(err)
	}
	duplicate := diffFile(t, dir, "duplicate.json", cliResource("Credential", "private"))
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, args := range [][]string{
		{"-f", first, "-f", malformed}, {"-f", first, "-f", duplicate}, {"-f", "-", "-f", "-"}, {"-f", first, "--max-source-bytes", "32"}, {"-f", first, "--max-staging-bytes", "32"}, {"-f", first, "--max-source-bytes", "-1"}, {}, {"--unknown-private-flag"}, {"-f", first, "-o", "unsupported"},
		{"https://example.test/source?credential=" + cliSecretValue}, {"-f", first, "-o", cliSecretValue},
	} {
		stdout, stderr, err := fixture.run(t, nil, append([]string{"diff"}, args...)...)
		if err == nil || ExitCode(err) != 2 || strings.Contains(stdout+stderr+err.Error(), cliSecretValue) {
			t.Fatal("invalid input did not fail safely", err)
		}
		assertDiffSpoolRemoved(t, spool)
	}
	if calls.Load() != 0 {
		t.Fatal("an incomplete collection reached the API")
	}
}

func TestDiffExplicitDirectoryRecursionAndOverlappingInputs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	first := diffFile(t, dir, "a.json", cliResource("Monitor", "first"))
	diffFile(t, dir, "z.yaml", cliResource("Monitor", "last"))
	diffFile(t, sub, "b.json", cliResource("Monitor", "nested"))
	diffFile(t, dir, "ignored.txt", cliResource("Monitor", "ignored"))
	var count atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		result := diffResponse(t, r)
		count.Store(int32(len(result.Items)))
		_ = json.NewEncoder(w).Encode(result)
	})
	for _, recursive := range []bool{false, true} {
		args := []string{"diff", "-f", dir, "-f", first, "-o", "json"}
		want := int32(2)
		if recursive {
			args = append(args, "-R")
			want = 3
		}
		_, _, err := fixture.run(t, nil, args...)
		if !errors.Is(err, ErrDifferences) || count.Load() != want {
			t.Fatal("directory recursion or source deduplication differs from SDK", err)
		}
	}
}

func TestDiffRemoteInputUsesSeparateUnauthenticatedClient(t *testing.T) {
	spool := diffSpool(t)
	raw, _ := json.Marshal(cliResource("Monitor", "remote"))
	var sourceCalls, apiCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("source fetch inherited API authentication")
		}
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/manifest?signature=private-query,second", http.StatusFound)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer source.Close()
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		_ = json.NewEncoder(w).Encode(diffResponse(t, r))
	})
	address := source.URL + "/start?signature=private-query,second"
	_, _, err := fixture.run(t, nil, "diff", "-f", address)
	if ExitCode(err) != 2 || sourceCalls.Load() != 0 || apiCalls.Load() != 0 {
		t.Fatal("HTTP source did not require separate opt-in")
	}
	stdout, stderr, err := fixture.run(t, nil, "diff", "-f", address, "--allow-http-sources", "-o", "json")
	if !errors.Is(err, ErrDifferences) || sourceCalls.Load() != 2 || apiCalls.Load() != 1 || strings.Contains(stdout+stderr, "private-query") || strings.Contains(stdout, "signature=") {
		t.Fatal("source client isolation or output redaction failed", err)
	}
	assertDiffSpoolRemoved(t, spool)
}

func TestDiffGraphFailureIncludesSafeLocalSourceCoordinates(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		result := diffResponse(t, r)
		result.Valid = false
		result.Items[0].Outcome = "invalid"
		result.Errors = []api.FieldError{{Field: "items[0].resource", Reason: "missingReference", Message: cliSecretValue}}
		_ = json.NewEncoder(w).Encode(result)
	})
	raw, _ := json.Marshal(cliResource("Recipient", "oncall"))
	stdout, stderr, err := fixture.run(t, raw, "diff", "-f", "-", "-o", "json")
	var result diffReport
	if ExitCode(err) != 2 || !errors.Is(err, collection.ErrPreflightRejected) || json.Unmarshal([]byte(stdout), &result) != nil || result.Valid || len(result.Errors) != 1 || result.Errors[0].Source == nil || result.Errors[0].Source.File != "stdin" || result.Errors[0].Source.Document != 1 || result.Errors[0].Source.Item != 1 || result.Errors[0].ID != "Recipient/oncall" || strings.Contains(stdout+stderr+err.Error(), cliSecretValue) {
		t.Fatal("invalid graph lost attribution or leaked detail", err)
	}
}

func TestDiffPermissionAndLostReplyAreNotRetried(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "lost reply"}[lost], func(t *testing.T) {
			spool := diffSpool(t)
			var calls atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if lost {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(api.Problem{Type: "about:blank", Title: "Forbidden", Status: 403, Code: "forbidden", Detail: cliSecretValue})
			})
			raw, _ := json.Marshal(cliResource("Monitor", "one"))
			stdout, stderr, err := fixture.run(t, raw, "diff", "-f", "-")
			if ExitCode(err) != 2 || calls.Load() != 1 || strings.Contains(stdout+stderr+err.Error(), cliSecretValue) {
				t.Fatal("request was retried or error detail leaked", err)
			}
			if lost {
				var transport *cpra.TransportError
				if errors.Is(err, cpra.ErrAmbiguous) || !errors.As(err, &transport) {
					t.Fatal("lost response classification lost", err)
				}
			} else {
				var typed *cpra.Error
				if !errors.Is(err, cpra.ErrUnauthorized) || !errors.As(err, &typed) {
					t.Fatal("typed API permission failure lost", err)
				}
			}
			assertDiffSpoolRemoved(t, spool)
		})
	}
}

func TestDiffCancellationRemovesFrozenInputs(t *testing.T) {
	spool := diffSpool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		cancel()
		<-r.Context().Done()
	})
	raw, _ := json.Marshal(cliResource("Credential", "private"))
	root := NewRootCommand()
	root.SetIn(bytes.NewReader(raw))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"--server", fixture.server.URL, "--ca-file", fixture.ca, "--token-file", fixture.token, "diff", "-f", "-"})
	err := root.ExecuteContext(ctx)
	if ExitCode(err) != 2 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	assertDiffSpoolRemoved(t, spool)
}

type diffBlockedInput struct {
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (r *diffBlockedInput) Read(p []byte) (int, error) {
	close(r.entered)
	<-r.release
	n := copy(p, []byte("late bytes"))
	close(r.finished)
	return n, nil
}
func TestDiffNativeStdinCancellationDoesNotRetainCallerBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := &diffBlockedInput{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	reader := diffStdinReader{ctx: ctx, reader: input}
	buffer := bytes.Repeat([]byte{'x'}, 32)
	done := make(chan error, 1)
	go func() { _, err := reader.Read(buffer); done <- err }()
	select {
	case <-input.entered:
	case <-time.After(time.Second):
		t.Fatal("read never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("blocked read lost cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native stdin cancellation waited for an uninterruptible read")
	}
	close(input.release)
	<-input.finished
	if !bytes.Equal(buffer, bytes.Repeat([]byte{'x'}, 32)) {
		t.Fatal("abandoned read modified returned caller memory")
	}
}
