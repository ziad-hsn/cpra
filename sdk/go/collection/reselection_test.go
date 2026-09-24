package collection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const reselectOperationID = "op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000001"
const reselectAttemptID = "f328f33c-e060-482c-baf5-108ca01a704f"

type reselectionFixture struct {
	operation               api.Operation
	attempt                 api.CollectionReselectionAttempt
	raw, received, borrowed [][]byte
	calls                   []string
	fault                   string
	async                   bool
	retry                   time.Duration
	reads                   int
	afterCreate             func()
	change                  func(string, *api.CollectionReselectionAttempt)
	changeOperation         func(int, *api.Operation)
	getCalls                int
}

func newReselectionFixture(raw ...[]byte) *reselectionFixture {
	return &reselectionFixture{raw: raw, operation: api.Operation{ID: reselectOperationID, IdentityFormat: commitment.Format, NormalizationProfile: FileNormalizationProfile, ContentDigest: strings.Repeat("a", 64), ItemCount: api.Pointer(int64(3)), Uploaded: api.Pointer(int64(1)), Committed: api.Pointer(int64(0)), Applied: api.Pointer(int64(0)), State: "uploading"},
		attempt: api.CollectionReselectionAttempt{ID: reselectAttemptID, OperationID: reselectOperationID, NormalizationProfile: FileNormalizationProfile, SourceCount: int64(len(raw)), NextSource: 1, OperationUploaded: 1, Phase: "uploading", ExpiresAt: time.Now().Add(time.Hour)}}
}
func (f *reselectionFixture) Get(_ context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "get")
	f.getCalls++
	o := f.operation
	if f.changeOperation != nil {
		f.changeOperation(f.getCalls, &o)
	}
	return &cpra.Response[api.Operation]{OperationID: id, Data: o}, nil
}
func (f *reselectionFixture) reply(method string) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	if f.fault == method {
		return nil, &cpra.AmbiguousError{OperationID: reselectOperationID, Cause: errors.New("PRIVATE-REMOTE-ERROR")}
	}
	a := f.attempt
	if f.change != nil {
		f.change(method, &a)
	}
	return &cpra.Response[api.CollectionReselectionAttempt]{OperationID: reselectOperationID, Data: a, RetryAfter: f.retry}, nil
}
func (f *reselectionFixture) CreateReselection(_ context.Context, _ string, r api.CollectionReselectionCreateRequest) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	f.calls = append(f.calls, "create")
	if r.SourceCount != int64(len(f.raw)) || r.NormalizationProfile != FileNormalizationProfile {
		panic("unexpected source creation identity")
	}
	f.received = make([][]byte, len(f.raw))
	if f.afterCreate != nil {
		f.afterCreate()
	}
	return f.reply("create")
}
func (f *reselectionFixture) GetReselection(_ context.Context, _, _ string) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	f.calls = append(f.calls, "attempt")
	f.reads++
	if f.async && f.reads > 1 {
		if f.attempt.Phase == "verifying" {
			f.attempt.Phase = "verified"
		} else if f.attempt.Phase == "transferring" {
			f.attempt.Phase = "completed"
			f.attempt.OperationUploaded = *f.operation.ItemCount
			*f.operation.Uploaded = *f.operation.ItemCount
		}
	}
	return f.reply("attempt")
}
func (f *reselectionFixture) UploadReselectionSource(_ context.Context, _, _ string, p cpra.ReselectionSourcePart) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	f.calls = append(f.calls, fmt.Sprintf("put:%d:%d:%t", p.Source, p.Offset, p.End))
	f.borrowed = append(f.borrowed, p.Data)
	if len(p.Data) > cpra.MaxReselectionSourcePartBytes || p.Source != f.attempt.NextSource || p.Offset != f.attempt.NextOffset {
		panic("unexpected upload coordinates")
	}
	f.received[p.Source-1] = append(f.received[p.Source-1], p.Data...)
	f.attempt.RawBytes += int64(len(p.Data))
	f.attempt.NextOffset += int64(len(p.Data))
	if p.End {
		f.attempt.SourcesCompleted++
		f.attempt.NextOffset = 0
		f.attempt.NextSource++
		if f.attempt.SourcesCompleted == f.attempt.SourceCount {
			f.attempt.NextSource = 0
		}
	}
	return f.reply("put")
}
func (f *reselectionFixture) VerifyReselection(_ context.Context, _, _ string) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	f.calls = append(f.calls, "verify")
	f.attempt.Phase = "verified"
	for n := range f.raw {
		if !bytes.Equal(f.raw[n], f.received[n]) {
			f.attempt.Phase = "failed"
			f.attempt.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("input_mismatch"))
		}
	}
	if f.async && f.attempt.Phase == "verified" {
		f.attempt.Phase = "verifying"
		f.reads = 0
	}
	return f.reply("verify")
}
func (f *reselectionFixture) ResumeReselection(_ context.Context, _, _ string) (*cpra.Response[api.CollectionReselectionAttempt], error) {
	f.calls = append(f.calls, "resume")
	f.attempt.ErrorCode = nil
	f.attempt.Phase = "completed"
	f.attempt.OperationUploaded = *f.operation.ItemCount
	*f.operation.Uploaded = *f.operation.ItemCount
	if f.async {
		f.attempt.Phase = "transferring"
		f.attempt.OperationUploaded = 1
		*f.operation.Uploaded = 1
		f.reads = 0
	}
	return f.reply("resume")
}
func reselectionTestSources(raw [][]byte) []Source {
	var out []Source
	for _, r := range raw {
		out = append(out, Reader("PRIVATE-LOCAL-PATH", bytes.NewReader(r)))
	}
	return out
}
func reselectionTestOptions(t *testing.T) ReselectionOptions {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("private source spool remained: %v entries=%d", err, len(entries))
		}
	})
	return ReselectionOptions{Sources: Options{TempDir: dir}}
}
func TestReselectOriginalUploadAndPrivateSourceFreeze(t *testing.T) {
	raw := [][]byte{bytes.Repeat([]byte("x"), cpra.MaxReselectionSourcePartBytes+2), {}, []byte("# original comment\nPRIVATE-RESOURCE\n")}
	f := newReselectionFixture(raw...)
	opts := reselectionTestOptions(t)
	sourceDir := t.TempDir()
	var sources []Source
	for n, r := range raw {
		path := filepath.Join(sourceDir, fmt.Sprintf("%d.yaml", n))
		if err := os.WriteFile(path, r, 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, File(path))
	}
	f.afterCreate = func() {
		for n := range raw {
			if err := os.WriteFile(filepath.Join(sourceDir, fmt.Sprintf("%d.yaml", n)), []byte("changed after freezing"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := Reselect(t.Context(), f, reselectOperationID, sources, opts)
	if err != nil || !result.Complete || result.OperationID != reselectOperationID || result.Operation.State != "uploading" || *result.Operation.Uploaded != 3 || result.Attempt.ID != reselectAttemptID {
		t.Fatalf("original upload failed: %v %v", result, err)
	}
	want := []string{"get", "create", "put:1:0:false", "put:1:1048576:true", "put:2:0:true", "put:3:0:true", "verify", "resume", "get"}
	if !reflect.DeepEqual(f.calls, want) || !reflect.DeepEqual(f.raw[0], f.received[0]) || !bytes.Equal(f.raw[2], f.received[2]) {
		t.Fatalf("wrong protocol sequence: %v", f.calls)
	}
	for _, part := range f.borrowed {
		for _, b := range part {
			if b != 0 {
				t.Fatal("part buffer retained input")
			}
		}
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, result), "PRIVATE-") {
			t.Fatal("result formatting leaked input")
		}
	}
}
func TestReselectLostMutationRepliesStopAndKeepKnownAttempt(t *testing.T) {
	for _, fault := range []string{"create", "put", "verify", "resume"} {
		t.Run(fault, func(t *testing.T) {
			f := newReselectionFixture([]byte("original"))
			f.fault = fault
			opts := reselectionTestOptions(t)
			result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), opts)
			if !errors.Is(err, cpra.ErrAmbiguous) || result.Complete || result.OperationID != reselectOperationID {
				t.Fatal("lost reply was not uncertain", err)
			}
			if (result.Attempt == nil) != (fault == "create") {
				t.Fatal("known attempt identity not preserved")
			}
			for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
				if strings.Contains(fmt.Sprintf(format, err), "PRIVATE-") {
					t.Fatal("error formatting leaked")
				}
			}
			calls := strings.Join(f.calls, ",")
			if strings.Count(calls, fault) == 0 && fault != "put" {
				t.Fatal(calls)
			}
			if fault == "create" {
				if len(f.calls) != 2 {
					t.Fatal("unknown create retried", f.calls)
				}
				return
			}
			f.fault = ""
			f.calls = nil
			opts.AttemptID = reselectAttemptID
			result, err = Reselect(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), opts)
			if err != nil || !result.Complete {
				t.Fatal("explicit invocation did not reconcile", err, f.calls)
			}
			for _, call := range f.calls {
				if call == "create" || fault == "put" && strings.HasPrefix(call, "put:") || fault == "verify" && call == "verify" || fault == "resume" && call == "resume" {
					t.Fatal("repeated already committed mutation", f.calls)
				}
			}
		})
	}
}
func TestReselectRejectsUnsupportedOriginalBeforeSourcesOrWrites(t *testing.T) {
	for _, mode := range []string{"unprofiled", "unknown-profile", "unknown-format", "unknown-state", "canceled", "missing-count", "missing-uploaded", "wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			f := newReselectionFixture([]byte("one"))
			switch mode {
			case "unprofiled":
				f.operation.NormalizationProfile = ""
			case "unknown-profile":
				f.operation.NormalizationProfile = "future"
			case "unknown-format":
				f.operation.IdentityFormat = "future"
			case "unknown-state":
				f.operation.State = "future"
			case "canceled":
				f.operation.State = "canceled"
			case "missing-count":
				f.operation.ItemCount = nil
			case "missing-uploaded":
				f.operation.Uploaded = nil
			case "wrong-id":
				f.operation.ID = "other"
			}
			result, err := Reselect(t.Context(), f, reselectOperationID, []Source{File("PRIVATE-NONEXISTENT")}, reselectionTestOptions(t))
			if err == nil || result.Complete || !reflect.DeepEqual(f.calls, []string{"get"}) {
				t.Fatal("unsupported original reached sources/write", err, f.calls)
			}
		})
	}
}
func TestReselectFullOriginalSkipsGoneAttemptAndUnusedSources(t *testing.T) {
	f := newReselectionFixture([]byte("one"))
	*f.operation.Uploaded = 3
	f.operation.State = "canceled"
	f.operation.Items = []api.ApplyResult{{ID: "Monitor/x", Outcome: "failed", Message: "PRIVATE-PROVIDER"}}
	opts := reselectionTestOptions(t)
	opts.AttemptID = reselectAttemptID
	result, err := Reselect(t.Context(), f, reselectOperationID, []Source{File("PRIVATE-NONEXISTENT")}, opts)
	raw, _ := json.Marshal(result)
	if err != nil || !result.Complete || result.Attempt != nil || !reflect.DeepEqual(f.calls, []string{"get"}) || strings.Contains(string(raw), "PRIVATE-") {
		t.Fatal("full upload did extra work or leaked", err, f.calls)
	}
}
func TestReselectRawLimitsAndReadFailureBeforeMutation(t *testing.T) {
	for _, mode := range []string{"bytes", "staging", "too-many", "directory-count", "source-read", "source-open", "no-sources", "configured-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newReselectionFixture([]byte("four"))
			opts := reselectionTestOptions(t)
			sources := reselectionTestSources(f.raw)
			switch mode {
			case "bytes":
				opts.Sources.MaxSourceBytes = 3
			case "staging":
				opts.Sources.MaxStagingBytes = 3
			case "too-many":
				sources = make([]Source, 1001)
			case "directory-count":
				dir := t.TempDir()
				for n := 0; n < 1001; n++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.yaml", n)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				sources = []Source{File(dir)}
			case "source-read":
				sources = append(sources, Reader("PRIVATE-SOURCE", reselectionErrorReader{}))
			case "source-open":
				sources = append(sources, File("PRIVATE-NONEXISTENT"))
			case "no-sources":
				sources = nil
			case "configured-limit":
				opts.Sources.MaxSourceBytes = reselectionRawBytes + 1
			}
			result, err := Reselect(t.Context(), f, reselectOperationID, sources, opts)
			if err == nil || result.Complete || !reflect.DeepEqual(f.calls, []string{"get"}) || strings.Contains(fmt.Sprintf("%+v", err), "PRIVATE-") {
				t.Fatal("invalid input mutated operation", err, f.calls)
			}
		})
	}
}

type reselectionErrorReader struct{}

func (reselectionErrorReader) Read([]byte) (int, error) { return 0, errors.New("PRIVATE-INPUT-ERROR") }
func TestReselectChangedLastSourceFailsBeforeResume(t *testing.T) {
	f := newReselectionFixture([]byte("original"), nil)
	result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources([][]byte{f.raw[0], []byte("# changed empty file")}), reselectionTestOptions(t))
	if !errors.Is(err, ErrReselectionState) || result.Complete || result.Attempt == nil || result.Attempt.ErrorCode == nil || *result.Attempt.ErrorCode != "input_mismatch" || *f.operation.Uploaded != 1 || strings.Contains(strings.Join(f.calls, ","), "resume") {
		t.Fatal("changed raw input resumed upload", err, f.calls)
	}
}
func TestReselectKnownAttemptPhasesAndPollTiming(t *testing.T) {
	for _, phase := range []string{"verifying", "verified", "transferring", "completed"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newReselectionFixture([]byte("raw"))
				f.async = true
				f.retry = 8 * time.Second
				f.attempt.Phase = api.CollectionReselectionAttemptPhase(phase)
				f.attempt.SourcesCompleted = 1
				f.attempt.NextSource = 0
				f.attempt.RawBytes = 3
				if phase == "completed" {
					f.attempt.OperationUploaded = 3
					f.changeOperation = func(n int, o *api.Operation) {
						if n > 1 {
							o.Uploaded = api.Pointer(int64(3))
						}
					}
				}
				opts := reselectionTestOptions(t)
				opts.AttemptID = reselectAttemptID
				start := time.Now()
				result, err := Reselect(t.Context(), f, reselectOperationID, []Source{File("PRIVATE-UNUSED")}, opts)
				if err != nil || !result.Complete {
					t.Fatal("known attempt did not finish", err, f.calls)
				}
				elapsed := time.Since(start)
				if (phase == "verifying" || phase == "transferring") && elapsed < 8*time.Second {
					t.Fatal("poll ignored retry-after", elapsed)
				}
				for _, call := range f.calls {
					if call == "create" || call == "verify" || strings.HasPrefix(call, "put:") {
						t.Fatal("known admitted input was resent", f.calls)
					}
				}
				if strings.Count(strings.Join(f.calls, ","), "resume") > 1 {
					t.Fatal("resume submitted twice")
				}
			})
		})
	}
}
func TestReselectCancellationStopsPollingWithoutAnotherMutation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newReselectionFixture([]byte("raw"))
		f.attempt.Phase = "verifying"
		f.attempt.SourcesCompleted = 1
		f.attempt.NextSource = 0
		f.attempt.RawBytes = 3
		opts := reselectionTestOptions(t)
		opts.AttemptID = reselectAttemptID
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		result, err := Reselect(ctx, f, reselectOperationID, nil, opts)
		if !errors.Is(err, context.DeadlineExceeded) || result.Attempt == nil || !reflect.DeepEqual(f.calls, []string{"get", "attempt"}) {
			t.Fatal("cancellation retried/changed server work", err, f.calls)
		}
	})
}
func TestReselectRejectsChangedOrBackwardAttemptMetadata(t *testing.T) {
	for _, mode := range []string{"id", "operation", "profile", "source-count", "expiry", "bytes", "uploaded", "phase", "verify-stale", "resume-stale", "ready-count"} {
		t.Run(mode, func(t *testing.T) {
			f := newReselectionFixture([]byte("raw"))
			f.change = func(method string, a *api.CollectionReselectionAttempt) {
				if method == "put" {
					switch mode {
					case "id":
						a.ID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
					case "operation":
						a.OperationID = "other"
					case "profile":
						a.NormalizationProfile = "future"
					case "source-count":
						a.SourceCount++
					case "expiry":
						a.ExpiresAt = a.ExpiresAt.Add(time.Second)
					case "bytes":
						a.RawBytes--
					case "uploaded":
						a.OperationUploaded--
					case "phase":
						a.Phase = "future"
					}
				}
				if method == "verify" && mode == "verify-stale" {
					a.Phase = "uploading"
				}
				if method == "resume" && mode == "resume-stale" {
					a.Phase = "verified"
				}
				if method == "resume" && mode == "ready-count" {
					a.OperationUploaded = 2
				}
			}
			result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), reselectionTestOptions(t))
			if err == nil || result.Complete {
				t.Fatal("changed metadata accepted", f.calls)
			}
			if mode != "ready-count" && !errors.Is(err, cpra.ErrAmbiguous) {
				t.Fatal("mutation response mismatch not uncertain", err)
			}
		})
	}
}
func TestReselectRawDirectoryOrder(t *testing.T) {
	// Directory order is lexical, while explicit selections retain caller order.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "z.yaml"), []byte("last"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := freezeReselectionSources(t.Context(), []Source{Reader("first", strings.NewReader("first")), File(dir)}, Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if len(s.records) != 3 || s.records[0].size != 5 || s.records[1].size != 0 || s.records[2].size != 4 {
		t.Fatal("source boundaries/order changed")
	}
	data, err := io.ReadAll(io.NewSectionReader(s.file, 0, 9))
	if err != nil || string(data) != "firstlast" {
		t.Fatal("raw bytes changed")
	}
}

func TestReselectURLSourceUsesSeparateUnauthenticatedClient(t *testing.T) {
	raw := []byte("# original URL input\n")
	var sourceCalls int
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("source fetch inherited credentials")
		}
		_, _ = w.Write(raw)
	}))
	defer source.Close()
	f := newReselectionFixture(raw)
	opts := reselectionTestOptions(t)
	opts.Sources.AllowHTTP = true
	result, err := Reselect(t.Context(), f, reselectOperationID, []Source{URL(source.URL + "/private?PRIVATE-QUERY")}, opts)
	if err != nil || !result.Complete || sourceCalls != 1 {
		t.Fatal("source was not acquired separately once", err, sourceCalls)
	}
}

type reselectionCancelReader struct{ cancel context.CancelFunc }

func (r reselectionCancelReader) Read(p []byte) (int, error) { r.cancel(); p[0] = 'x'; return 1, nil }
func TestReselectCanceledRawFreezeRemovesStagingBeforeMutation(t *testing.T) {
	f := newReselectionFixture([]byte("x"))
	opts := reselectionTestOptions(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := Reselect(ctx, f, reselectOperationID, []Source{Reader("private", reselectionCancelReader{cancel})}, opts)
	if !errors.Is(err, context.Canceled) || result.Complete || !reflect.DeepEqual(f.calls, []string{"get"}) {
		t.Fatal("canceled input reached mutations", err, f.calls)
	}
}

func TestReselectAttemptDeadlineAndMinimumPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newReselectionFixture([]byte("raw"))
		f.attempt.Phase = "verifying"
		f.attempt.SourcesCompleted = 1
		f.attempt.NextSource = 0
		f.attempt.RawBytes = 3
		f.attempt.ExpiresAt = time.Now().Add(4 * time.Second)
		opts := reselectionTestOptions(t)
		opts.AttemptID = reselectAttemptID
		start := time.Now()
		result, err := Reselect(t.Context(), f, reselectOperationID, nil, opts)
		if !errors.Is(err, context.DeadlineExceeded) || result.Complete || time.Since(start) != 4*time.Second || !reflect.DeepEqual(f.calls, []string{"get", "attempt"}) {
			t.Fatal("attempt deadline extended or polled below minimum", err, f.calls, time.Since(start))
		}
	})
}

func TestReselectFinalOriginalIdentityAndCountsMustMatch(t *testing.T) {
	for _, field := range []string{"id", "digest", "profile", "count", "uploaded"} {
		t.Run(field, func(t *testing.T) {
			f := newReselectionFixture([]byte("raw"))
			f.changeOperation = func(n int, o *api.Operation) {
				if n < 2 {
					return
				}
				switch field {
				case "id":
					o.ID = "other"
				case "digest":
					o.ContentDigest = strings.Repeat("b", 64)
				case "profile":
					o.NormalizationProfile = ""
				case "count":
					o.ItemCount = api.Pointer(int64(4))
				case "uploaded":
					o.Uploaded = api.Pointer(int64(2))
				}
			}
			result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), reselectionTestOptions(t))
			if !errors.Is(err, ErrReselectionObservation) || result.Complete || result.Operation.ContentDigest != strings.Repeat("a", 64) || *result.Operation.Uploaded != 1 {
				t.Fatal("final read lost original identity fence", err, result)
			}
		})
	}
}

func TestReselectExplicitFailedTransferCanResumeOnce(t *testing.T) {
	for _, failAgain := range []bool{false, true} {
		t.Run(fmt.Sprint(failAgain), func(t *testing.T) {
			f := newReselectionFixture([]byte("raw"))
			f.attempt.Phase = "failed"
			f.attempt.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("transfer_failed"))
			f.attempt.SourcesCompleted = 1
			f.attempt.NextSource = 0
			f.attempt.RawBytes = 3
			if failAgain {
				f.change = func(method string, a *api.CollectionReselectionAttempt) {
					if method == "resume" {
						a.Phase = "failed"
						a.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("transfer_failed"))
					}
				}
			}
			opts := reselectionTestOptions(t)
			opts.AttemptID = reselectAttemptID
			result, err := Reselect(t.Context(), f, reselectOperationID, []Source{File("PRIVATE-UNUSED")}, opts)
			if failAgain {
				if !errors.Is(err, ErrReselectionState) || result.Complete {
					t.Fatal("second transfer failure was retried", err)
				}
			} else if err != nil || !result.Complete {
				t.Fatal("explicit transfer retry failed", err)
			}
			if strings.Count(strings.Join(f.calls, ","), "resume") != 1 {
				t.Fatal("explicit transfer retry count changed", f.calls)
			}
		})
	}
}
func TestReselectNewTransferFailureNeverRetries(t *testing.T) {
	f := newReselectionFixture([]byte("raw"))
	f.change = func(method string, a *api.CollectionReselectionAttempt) {
		if method == "resume" {
			a.Phase = "failed"
			a.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("transfer_failed"))
		}
	}
	result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources(f.raw), reselectionTestOptions(t))
	if !errors.Is(err, ErrReselectionState) || result.Complete || strings.Count(strings.Join(f.calls, ","), "resume") != 1 {
		t.Fatal("new failed transfer retried", err, f.calls)
	}
}
func TestReselectKnownInputFailureDoesNotRetryTransfer(t *testing.T) {
	f := newReselectionFixture([]byte("raw"))
	f.attempt.Phase = "failed"
	f.attempt.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("input_mismatch"))
	opts := reselectionTestOptions(t)
	opts.AttemptID = reselectAttemptID
	result, err := Reselect(t.Context(), f, reselectOperationID, nil, opts)
	if !errors.Is(err, ErrReselectionState) || result.Complete || !reflect.DeepEqual(f.calls, []string{"get", "attempt"}) {
		t.Fatal("input proof failure was retried", err, f.calls)
	}
}
func TestReselectKnownRawPrefixRemainsAuthoritative(t *testing.T) {
	f := newReselectionFixture([]byte("original"))
	f.received = [][]byte{[]byte("orig")}
	f.attempt.RawBytes = 4
	f.attempt.NextOffset = 4
	opts := reselectionTestOptions(t)
	opts.AttemptID = reselectAttemptID
	// The new selection's first four bytes are not resent or attested. The server
	// still proves its retained original bytes plus the supplied missing suffix.
	result, err := Reselect(t.Context(), f, reselectOperationID, reselectionTestSources([][]byte{[]byte("EDITinal")}), opts)
	if err != nil || !result.Complete || !bytes.Equal(f.received[0], f.raw[0]) || !reflect.DeepEqual(f.calls, []string{"get", "attempt", "put:1:4:true", "verify", "resume", "get"}) {
		t.Fatal("known source prefix was replaced", err, f.calls)
	}
}
