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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"gopkg.in/yaml.v3"
)

const cliUploadAttemptID = "94075978-f3c6-4245-aab6-cff61781f020"

type uploadRecoveryCLIProtocol struct {
	t         *testing.T
	mu        sync.Mutex
	calls     []string
	operation api.Operation
	attempt   api.CollectionReselectionAttempt
	parts     [][]byte
	loss      string
}

func newUploadRecoveryCLIProtocol(t *testing.T, phase string) *uploadRecoveryCLIProtocol {
	p := &uploadRecoveryCLIProtocol{t: t,
		operation: api.Operation{ID: sequencedOperationID, State: "uploading", IdentityFormat: commitment.Format, NormalizationProfile: collection.FileNormalizationProfile, ContentDigest: strings.Repeat("a", 64), ItemCount: api.Pointer(int64(2)), Uploaded: api.Pointer(int64(1)), Committed: api.Pointer(int64(0)), Applied: api.Pointer(int64(0))},
		attempt:   api.CollectionReselectionAttempt{ID: cliUploadAttemptID, OperationID: sequencedOperationID, NormalizationProfile: collection.FileNormalizationProfile, Phase: api.CollectionReselectionAttemptPhase(phase), SourceCount: 1, NextSource: 1, OperationUploaded: 1, ExpiresAt: time.Now().UTC().Add(time.Minute)},
	}
	if phase != "uploading" {
		p.attempt.SourcesCompleted, p.attempt.NextSource, p.attempt.RawBytes = 1, 0, 10
	}
	if phase == "failed" {
		p.attempt.ErrorCode = api.Pointer(api.InputMismatch)
	}
	return p
}

func (p *uploadRecoveryCLIProtocol) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer named-operator-token" {
		p.t.Error("recovery lost API authentication")
	}
	base := "/api/v2/operations/" + sequencedOperationID
	path := base + "/reselection/" + cliUploadAttemptID
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Operation-ID", sequencedOperationID)
	action, status := "", http.StatusOK
	switch {
	case r.Method == "GET" && r.URL.Path == base:
		p.calls = append(p.calls, "get-original")
		_ = json.NewEncoder(w).Encode(p.operation)
		return
	case r.Method == "GET" && r.URL.Path == path:
		action = "get-attempt"
	case r.Method == "POST" && r.URL.Path == base+"/reselection":
		action, status = "create-attempt", http.StatusCreated
		var req api.CollectionReselectionCreateRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.NormalizationProfile != collection.FileNormalizationProfile {
			p.t.Error("invalid recovery create")
		}
		p.attempt.SourceCount = req.SourceCount
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, path+"/sources/"):
		action = "upload-source"
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			p.t.Error("source was not binary")
		}
		source, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, path+"/sources/"), 10, 64)
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if source != p.attempt.NextSource || offset != p.attempt.NextOffset {
			p.t.Error("changed source order or offset")
		}
		raw, _ := io.ReadAll(r.Body)
		p.parts = append(p.parts, raw)
		p.attempt.RawBytes += int64(len(raw))
		p.attempt.NextOffset += int64(len(raw))
		if r.URL.Query().Get("end") == "true" {
			p.attempt.SourcesCompleted++
			p.attempt.NextOffset = 0
			p.attempt.NextSource++
			if p.attempt.SourcesCompleted == p.attempt.SourceCount {
				p.attempt.NextSource = 0
			}
		}
	case r.Method == "POST" && r.URL.Path == path+"/verify":
		action, status = "verify", http.StatusAccepted
		p.attempt.Phase = "verified"
	case r.Method == "POST" && r.URL.Path == path+"/resume":
		action, status = "resume", http.StatusAccepted
		p.attempt.Phase, p.attempt.OperationUploaded = "completed", 2
		p.operation.Uploaded = api.Pointer(int64(2))
	default:
		p.t.Errorf("upload recovery called unsupported operation: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
		return
	}
	p.calls = append(p.calls, action)
	if action == p.loss {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			p.t.Error(err)
			return
		}
		_ = connection.Close()
		return
	}
	w.WriteHeader(status)
	raw, _ := json.Marshal(p.attempt)
	var safe map[string]any
	_ = json.Unmarshal(raw, &safe)
	safe["sourcePath"], safe["resource"], safe["diagnostic"] = "/PRIVATE-SOURCE", cliSecretValue, cliSecretValue
	_ = json.NewEncoder(w).Encode(safe)
}

func parseUploadRecoveryReport(t *testing.T, stdout, format string) uploadRecoveryReport {
	t.Helper()
	if format == "yaml" {
		var value map[string]any
		if err := yaml.Unmarshal([]byte(stdout), &value); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		stdout = string(raw)
	}
	var report uploadRecoveryReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal("invalid recovery report", err)
	}
	return report
}

func assertUploadRecoveryPrivate(t *testing.T, stdout, stderr string, err error) {
	t.Helper()
	message := stdout + stderr
	if err != nil {
		message += err.Error()
	}
	for _, private := range []string{cliSecretValue, "named-operator-token", "/PRIVATE-SOURCE", "sourcePath", "contentDigest", "identityKey"} {
		if strings.Contains(message, private) {
			t.Fatal("private input or response reached output")
		}
	}
}

func TestCollectionResumeUploadOnlyAndSourceOrder(t *testing.T) {
	for _, format := range []string{"json", "yaml", "table", "wide"} {
		t.Run(format, func(t *testing.T) {
			spool := diffSpool(t)
			dir := t.TempDir()
			first := diffFile(t, dir, "z.json", cliResource("Credential", "one"))
			second := diffFile(t, dir, "a.json", cliResource("Credential", "two"))
			p := newUploadRecoveryCLIProtocol(t, "uploading")
			f := newCLIManagementFixture(t, p.serve)
			stdout, stderr, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "-f", first, "-f", second, "-o", format)
			if err != nil {
				t.Fatal(err)
			}
			assertUploadRecoveryPrivate(t, stdout, stderr, err)
			if format == "json" || format == "yaml" {
				report := parseUploadRecoveryReport(t, stdout, format)
				if !report.Complete || report.OperationID != sequencedOperationID || report.AttemptID != cliUploadAttemptID || report.Operation == nil || *report.Operation.Uploaded != 2 || report.Attempt == nil || report.Attempt.Phase != "completed" {
					t.Fatal("lost original recovery progress")
				}
			} else if !strings.Contains(stdout, "Upload complete:") || !strings.Contains(stdout, "completed") || !strings.Contains(stdout, cliUploadAttemptID) {
				t.Fatal("missing safe table progress")
			}
			want := []string{"get-original", "create-attempt", "upload-source", "upload-source", "verify", "resume", "get-original"}
			if !reflect.DeepEqual(p.calls, want) || len(p.parts) != 2 || !bytes.Contains(p.parts[0], []byte(`"id":"one"`)) || !bytes.Contains(p.parts[1], []byte(`"id":"two"`)) {
				t.Fatal("changed source order, repeated mutation or activated configuration", p.calls)
			}
			assertDiffSpoolRemoved(t, spool)
		})
	}
}

func TestCollectionResumeUploadLostResponsesRetainHandles(t *testing.T) {
	for _, loss := range []string{"create-attempt", "upload-source", "verify", "resume"} {
		t.Run(loss, func(t *testing.T) {
			p := newUploadRecoveryCLIProtocol(t, "uploading")
			p.loss = loss
			f := newCLIManagementFixture(t, p.serve)
			stdout, stderr, err := f.run(t, []byte(cliSecretValue), "resume-upload", "operation/"+sequencedOperationID, "-f", "-", "-o", "json")
			if !errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("lost response was not uncertain", err)
			}
			report := parseUploadRecoveryReport(t, stdout, "json")
			if report.OperationID != sequencedOperationID || report.Complete || report.Operation == nil || *report.Operation.Uploaded != 1 {
				t.Fatal("uncertainty lost original progress")
			}
			if loss == "create-attempt" {
				if report.AttemptID != "" || report.Attempt != nil || !strings.Contains(err.Error(), "no attempt handle was returned") {
					t.Fatal("lost create invented an attempt handle")
				}
			} else if report.AttemptID != cliUploadAttemptID || report.Attempt == nil {
				t.Fatal("lost known attempt handle")
			}
			count := 0
			for _, call := range p.calls {
				if call == loss {
					count++
				}
			}
			if count != 1 || p.calls[len(p.calls)-1] != loss {
				t.Fatal("unknown mutation was retried or followed by another request", p.calls)
			}
			assertUploadRecoveryPrivate(t, stdout, stderr, err)
		})
	}
}

func TestCollectionResumeKnownAttemptReadsBeforeInput(t *testing.T) {
	for _, phase := range []string{"verified", "failed"} {
		t.Run(phase, func(t *testing.T) {
			p := newUploadRecoveryCLIProtocol(t, phase)
			f := newCLIManagementFixture(t, p.serve)
			stdout, stderr, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "--attempt", cliUploadAttemptID, "-f", "/PRIVATE-SOURCE/does-not-exist", "-o", "json")
			report := parseUploadRecoveryReport(t, stdout, "json")
			if report.AttemptID != cliUploadAttemptID || report.Attempt == nil || !reflect.DeepEqual(p.calls[:2], []string{"get-original", "get-attempt"}) {
				t.Fatal("did not preserve known attempt")
			}
			if phase == "verified" && (err != nil || !report.Complete || !reflect.DeepEqual(p.calls, []string{"get-original", "get-attempt", "resume", "get-original"})) {
				t.Fatal("verified attempt reopened inputs or changed operation", p.calls, err)
			}
			if phase == "failed" && (err == nil || report.Attempt.ErrorCode == nil || *report.Attempt.ErrorCode != api.InputMismatch || len(p.calls) != 2) {
				t.Fatal("failed attempt mutated or lost safe failure", p.calls, err)
			}
			assertUploadRecoveryPrivate(t, stdout, stderr, err)
		})
	}
}

func TestCollectionResumeDirectoryURLAndEmptySources(t *testing.T) {
	spool := diffSpool(t)
	dir := t.TempDir()
	_ = diffFile(t, dir, "b.json", cliResource("Credential", "second"))
	_ = diffFile(t, dir, "a.json", cliResource("Credential", "first"))
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	_ = diffFile(t, filepath.Join(dir, "nested"), "c.json", cliResource("Credential", "third"))
	p := newUploadRecoveryCLIProtocol(t, "uploading")
	var sourceRequests int
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("API credentials reached source URL")
		}
		_, _ = io.WriteString(w, "")
	}))
	defer source.Close()
	f := newCLIManagementFixture(t, p.serve)
	stdout, stderr, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "-R", "-f", dir, "-f", source.URL, "--allow-http-sources", "-o", "json")
	if err != nil || !parseUploadRecoveryReport(t, stdout, "json").Complete || sourceRequests != 1 || len(p.parts) != 4 || len(p.parts[3]) != 0 || p.attempt.SourceCount != 4 || p.attempt.SourcesCompleted != 4 {
		t.Fatal("directory, URL or empty source boundary changed", err, p.calls)
	}
	for index, id := range []string{"first", "second", "third"} {
		if !bytes.Contains(p.parts[index], []byte(`"id":"`+id+`"`)) {
			t.Fatal("directory expansion order changed")
		}
	}
	assertUploadRecoveryPrivate(t, stdout, stderr, err)
	assertDiffSpoolRemoved(t, spool)
}

func TestCollectionResumeQuotasAndUnprofiledNeverCreateAttempt(t *testing.T) {
	for _, flags := range [][]string{{"--max-source-bytes", "2"}, {"--max-staging-bytes", "2"}, {"--max-source-bytes", "67108865"}, {"-f", "-"}} {
		p := newUploadRecoveryCLIProtocol(t, "uploading")
		f := newCLIManagementFixture(t, p.serve)
		args := append([]string{"resume-upload", "operation/" + sequencedOperationID, "-f", "-", "-o", "json"}, flags...)
		stdout, stderr, err := f.run(t, []byte(cliSecretValue), args...)
		if err == nil {
			t.Fatal("invalid recovery source options accepted")
		}
		for _, call := range p.calls {
			if call != "get-original" {
				t.Fatal("input failure created or changed attempt", p.calls)
			}
		}
		assertUploadRecoveryPrivate(t, stdout, stderr, err)
	}
	p := newUploadRecoveryCLIProtocol(t, "uploading")
	p.operation.NormalizationProfile = ""
	f := newCLIManagementFixture(t, p.serve)
	stdout, stderr, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "-f", "/PRIVATE-SOURCE/missing", "-o", "json")
	if err == nil || !reflect.DeepEqual(p.calls, []string{"get-original"}) {
		t.Fatal("unprofiled original was relabeled", err, p.calls)
	}
	assertUploadRecoveryPrivate(t, stdout, stderr, err)
}

func TestCollectionResumeUploadingAttemptReadPrecedesSource(t *testing.T) {
	p := newUploadRecoveryCLIProtocol(t, "uploading")
	f := newCLIManagementFixture(t, p.serve)
	stdout, stderr, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "--attempt", cliUploadAttemptID, "-f", "/PRIVATE-SOURCE/missing", "-o", "json")
	report := parseUploadRecoveryReport(t, stdout, "json")
	if err == nil || report.Attempt == nil || !reflect.DeepEqual(p.calls, []string{"get-original", "get-attempt"}) {
		t.Fatal("source was acquired before known-attempt read", err, p.calls)
	}
	assertUploadRecoveryPrivate(t, stdout, stderr, err)
}

func TestCollectionResumeFullUploadNeedsNoSourcesOrAttempt(t *testing.T) {
	p := newUploadRecoveryCLIProtocol(t, "uploading")
	p.operation.Uploaded = api.Pointer(int64(2))
	f := newCLIManagementFixture(t, p.serve)
	stdout, _, err := f.run(t, nil, "resume-upload", "operation/"+sequencedOperationID, "--attempt", cliUploadAttemptID, "-o", "json")
	if err != nil || !parseUploadRecoveryReport(t, stdout, "json").Complete || !reflect.DeepEqual(p.calls, []string{"get-original"}) {
		t.Fatal("completed original depended on disappeared attempt", err, p.calls)
	}
}

func TestCollectionGetUploadAttemptReadOnlyAndRedacted(t *testing.T) {
	for _, format := range []string{"json", "yaml", "table", "wide"} {
		t.Run(format, func(t *testing.T) {
			p := newUploadRecoveryCLIProtocol(t, "failed")
			f := newCLIManagementFixture(t, p.serve)
			stdout, stderr, err := f.run(t, nil, "get", "upload-attempt", sequencedOperationID, cliUploadAttemptID, "-o", format)
			if err != nil || !reflect.DeepEqual(p.calls, []string{"get-attempt"}) || !strings.Contains(stdout, "input_mismatch") || !strings.Contains(stdout, cliUploadAttemptID) {
				t.Fatal("attempt read mutated or lost metadata", p.calls, err)
			}
			assertUploadRecoveryPrivate(t, stdout, stderr, err)
		})
	}
}

func TestCollectionResumeUnknownAttemptRetainsRequestedIDs(t *testing.T) {
	p := newUploadRecoveryCLIProtocol(t, "uploading")
	f := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/reselection/") {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, cliSecretValue)
			return
		}
		p.serve(w, r)
	})
	for _, args := range [][]string{{"resume-upload", "operation/" + sequencedOperationID, "--attempt", cliUploadAttemptID}, {"get", "upload-attempt", sequencedOperationID, cliUploadAttemptID}} {
		stdout, stderr, err := f.run(t, nil, append(args, "-o", "json")...)
		report := parseUploadRecoveryReport(t, stdout, "json")
		if err == nil || report.OperationID != sequencedOperationID || report.AttemptID != cliUploadAttemptID || report.Attempt != nil || report.Complete {
			t.Fatal("unknown attempt did not preserve requested handles", err)
		}
		assertUploadRecoveryPrivate(t, stdout, stderr, err)
	}
}

func TestCollectionProfileApplyExplicitAndUnsupported(t *testing.T) {
	for _, profile := range []string{"", collection.FileNormalizationProfile} {
		t.Run(profile, func(t *testing.T) {
			p := &applyCLIProtocol{t: t}
			var profiles []string
			f := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/collections/prepare" || r.URL.Path == "/api/v2/operations" {
					raw, _ := io.ReadAll(r.Body)
					var request struct {
						NormalizationProfile string `json:"normalizationProfile"`
					}
					_ = json.Unmarshal(raw, &request)
					profiles = append(profiles, request.NormalizationProfile)
					r.Body = io.NopCloser(bytes.NewReader(raw))
				}
				recorder := httptest.NewRecorder()
				p.serve(recorder, r)
				var response map[string]any
				_ = json.Unmarshal(recorder.Body.Bytes(), &response)
				if profile != "" && response["identityFormat"] != nil {
					response["normalizationProfile"] = profile
				}
				for name, values := range recorder.Header() {
					w.Header()[name] = values
				}
				w.WriteHeader(recorder.Code)
				_ = json.NewEncoder(w).Encode(response)
			})
			raw, _ := json.Marshal(cliResource("Monitor", "service"))
			args := []string{"apply", "-f", "-", "-o", "json"}
			if profile != "" {
				args = append(args, "--file-profile", profile)
			}
			stdout, _, err := f.run(t, raw, args...)
			if err != nil || !reflect.DeepEqual(profiles, []string{profile, profile}) || !strings.Contains(stdout, "applying") {
				t.Fatal("file profile opt-in was not bound to admission", profiles, err)
			}
		})
	}
	var calls int
	f := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })
	_, _, err := f.run(t, nil, "apply", "--file-profile", "PRIVATE-UNSUPPORTED", "-f", "/PRIVATE-SOURCE/no-file")
	if err == nil || calls != 0 || strings.Contains(err.Error(), "PRIVATE-") {
		t.Fatal("unsupported profile read input or reached API", err)
	}
}

func TestCollectionRecoveryHelpAndCanceledInput(t *testing.T) {
	for _, args := range [][]string{{"apply", "--help"}, {"resume-upload", "--help"}, {"get", "upload-attempt", "--help"}} {
		root := NewRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil || out.Len() == 0 {
			t.Fatal("help failed", err)
		}
		if args[0] == "apply" && (!strings.Contains(out.String(), "64 MiB with --file-profile") || !strings.Contains(out.String(), "512 MiB with --file-profile")) {
			t.Fatal("apply help omitted profile-specific byte defaults")
		}
	}
	f := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled command reached API") })
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetIn(bytes.NewReader(nil))
	root.SetArgs([]string{"--server", f.server.URL, "--ca-file", f.ca, "--token-file", f.token, "resume-upload", "operation/" + sequencedOperationID, "-f", "-"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := root.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
