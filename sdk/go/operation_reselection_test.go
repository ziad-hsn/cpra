package cpra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const reselectionTestOperation = "op.c47948b9-084b-4109-a862-0312e7a77102.00000000000000000001"
const reselectionTestAttempt = "f328f33c-e060-482c-baf5-108ca01a704f"

func reselectionTestObservation() api.CollectionReselectionAttempt {
	return api.CollectionReselectionAttempt{ID: reselectionTestAttempt, OperationID: reselectionTestOperation,
		NormalizationProfile: "cpra.file.base.v1", Phase: "uploading", SourceCount: 3, SourcesCompleted: 1,
		RawBytes: 17, NextSource: 2, ExpiresAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), OperationUploaded: 1}
}

func invokeReselection(t *testing.T, c *Client, name string) (int, string, error) {
	t.Helper()
	ctx := context.Background()
	var response *Response[api.CollectionReselectionAttempt]
	var err error
	switch name {
	case "CreateCollectionReselection":
		response, err = c.Operations.CreateReselection(ctx, reselectionTestOperation, api.CollectionReselectionCreateRequest{SourceCount: 3, NormalizationProfile: "cpra.file.base.v1"})
	case "GetCollectionReselection":
		response, err = c.Operations.GetReselection(ctx, reselectionTestOperation, reselectionTestAttempt)
	case "UploadCollectionReselectionSource":
		response, err = c.Operations.UploadReselectionSource(ctx, reselectionTestOperation, reselectionTestAttempt, ReselectionSourcePart{Source: 2, Offset: 7, End: true, Data: []byte("PRIVATE-RAW\x00\xff")})
	case "VerifyCollectionReselection":
		response, err = c.Operations.VerifyReselection(ctx, reselectionTestOperation, reselectionTestAttempt)
	case "ResumeCollectionReselection":
		response, err = c.Operations.ResumeReselection(ctx, reselectionTestOperation, reselectionTestAttempt)
	case "DiscardCollectionReselection":
		r, err := c.Operations.DiscardReselection(ctx, reselectionTestOperation, reselectionTestAttempt)
		if r == nil {
			return 0, "", err
		}
		return r.StatusCode, r.OperationID, err
	default:
		t.Fatalf("uncovered reselection operation %s", name)
	}
	if response == nil {
		return 0, "", err
	}
	return response.StatusCode, response.OperationID, err
}

func TestReselectionOperationInventory(t *testing.T) {
	covered := 0
	for _, op := range inventory(t) {
		if op.ContractTest != "TestReselectionOperationInventory" {
			continue
		}
		covered++
		t.Run(op.OperationID, func(t *testing.T) {
			if op.BuildTag != "" || !strings.HasPrefix(op.SDKMethod, "OperationsService.") {
				t.Fatal("reselection is part of the base SDK")
			}
			calls := 0
			status := http.StatusOK
			want := reselectionTestObservation()
			switch op.OperationID {
			case "CreateCollectionReselection":
				status = http.StatusCreated
				want.SourcesCompleted, want.RawBytes, want.NextSource = 0, 0, 1
			case "UploadCollectionReselectionSource":
				want.SourcesCompleted, want.NextSource, want.RawBytes = 2, 3, 20
			case "VerifyCollectionReselection", "ResumeCollectionReselection":
				status = http.StatusAccepted
				want.Phase, want.SourcesCompleted, want.NextSource = "verifying", want.SourceCount, 0
				if op.OperationID == "ResumeCollectionReselection" {
					want.Phase = "transferring"
				}
			case "DiscardCollectionReselection":
				status = http.StatusNoContent
			}
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := strings.NewReplacer("{id}", reselectionTestOperation, "{attempt}", reselectionTestAttempt, "{source}", "2").Replace(op.Path)
				if r.Method != op.Method || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("contract request identity changed")
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				switch op.OperationID {
				case "CreateCollectionReselection":
					var value map[string]any
					if json.Unmarshal(raw, &value) != nil || len(value) != 2 || value["sourceCount"] != float64(3) || value["normalizationProfile"] != "cpra.file.base.v1" {
						t.Error("creation metadata changed")
					}
				case "UploadCollectionReselectionSource":
					if r.Header.Get("Content-Type") != "application/octet-stream" || !bytes.Equal(raw, []byte("PRIVATE-RAW\x00\xff")) || r.URL.Query().Get("offset") != "7" || r.URL.Query().Get("end") != "true" || len(r.URL.Query()) != 2 {
						t.Error("binary source framing changed")
					}
				default:
					if len(raw) != 0 {
						t.Error("unexpected request body")
					}
				}
				if op.OperationID != "UploadCollectionReselectionSource" && r.URL.RawQuery != "" {
					t.Error("unexpected query metadata")
				}
				w.Header().Set("X-Operation-ID", reselectionTestOperation)
				w.WriteHeader(status)
				if status != http.StatusNoContent {
					_ = json.NewEncoder(w).Encode(want)
				}
			}, nil)
			got, id, err := invokeReselection(t, client, op.OperationID)
			if err != nil || got != status || id != reselectionTestOperation || calls != 1 {
				t.Fatal("contract failed", got, id, calls, err)
			}
		})
	}
	if covered != 6 {
		t.Fatal("incomplete reselection operation inventory", covered)
	}
}

func TestReselectionInputBoundsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) }, nil)
	for _, request := range []api.CollectionReselectionCreateRequest{
		{}, {SourceCount: 1, NormalizationProfile: ""}, {SourceCount: 1001, NormalizationProfile: "cpra.file.base.v1"},
		{SourceCount: 1, NormalizationProfile: "unknown"},
	} {
		if _, err := c.Operations.CreateReselection(t.Context(), reselectionTestOperation, request); err == nil {
			t.Fatal("invalid create request accepted")
		}
	}
	for _, part := range []ReselectionSourcePart{
		{Source: 0, End: true}, {Source: 1001, End: true}, {Source: 1, Offset: -1, End: true},
		{Source: 1, Offset: 1<<63 - 1, End: true}, {Source: 1},
		{Source: 1, End: true, Data: make([]byte, MaxReselectionSourcePartBytes+1)},
		{Source: 1, Offset: 64 << 20, End: true, Data: []byte{1}},
	} {
		if _, err := c.Operations.UploadReselectionSource(t.Context(), reselectionTestOperation, reselectionTestAttempt, part); err == nil {
			t.Fatal("invalid source part accepted")
		}
	}
	for _, id := range []string{"", "../escape", "source?secret", strings.Repeat("a", 129)} {
		if _, err := c.Operations.GetReselection(t.Context(), id, reselectionTestAttempt); err == nil {
			t.Fatal("invalid operation identity accepted")
		}
	}
	for _, attempt := range []string{"", "../escape", "not-a-uuid", strings.ToUpper(reselectionTestAttempt)} {
		if _, err := c.Operations.GetReselection(t.Context(), reselectionTestOperation, attempt); err == nil {
			t.Fatal("invalid attempt identity accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached transport")
	}
}

func TestReselectionEmptyEndAndExactBodyLimit(t *testing.T) {
	for _, size := range []int{0, MaxReselectionSourcePartBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := bytes.Repeat([]byte{'a'}, size)
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(raw, data) || r.URL.Query().Get("end") != "true" {
					t.Error("exact raw part changed", err)
				}
				want := reselectionTestObservation()
				want.RawBytes = int64(size)
				_ = json.NewEncoder(w).Encode(want)
			}, nil)
			if _, err := c.Operations.UploadReselectionSource(t.Context(), reselectionTestOperation, reselectionTestAttempt, ReselectionSourcePart{Source: 1, End: true, Data: data}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReselectionMutationFailureNeverRetries(t *testing.T) {
	for _, op := range inventory(t) {
		if op.ContractTest != "TestReselectionOperationInventory" || op.Method == http.MethodGet {
			continue
		}
		t.Run(op.OperationID, func(t *testing.T) {
			var calls atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"type":"about:blank","title":"Unavailable","status":503}`)
			}, func(config *Config) { config.ReadAttempts = 3 })
			_, _, err := invokeReselection(t, c, op.OperationID)
			var ambiguous *AmbiguousError
			if !errors.As(err, &ambiguous) || ambiguous.OperationID != reselectionTestOperation || calls.Load() != 1 {
				t.Fatal("write retried or lost original identity", calls.Load(), err)
			}
		})
	}
}

func TestReselectionResponseIdentityAndBounds(t *testing.T) {
	for _, invalid := range []string{"attempt", "operation", "header", "profile", "counts", "completed-position", "phase", "null-error", "omitted-zero", "overflow"} {
		t.Run(invalid, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				a := reselectionTestObservation()
				switch invalid {
				case "attempt":
					a.ID = "32b70a30-950a-416f-927e-903a962036fc"
				case "operation":
					a.OperationID = "wrong-parent"
				case "header":
					w.Header().Set("X-Operation-ID", "wrong-parent")
				case "profile":
					a.NormalizationProfile = "unsupported"
				case "counts":
					a.SourcesCompleted = a.SourceCount + 1
				case "completed-position":
					a.SourcesCompleted = a.SourceCount
				case "phase":
					a.Phase = "verified"
				case "overflow":
					_, _ = io.WriteString(w, strings.Repeat("x", reselectionResponseBytes+1))
					return
				}
				raw, _ := json.Marshal(a)
				if invalid == "null-error" {
					raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"errorCode":null}`)
				}
				if invalid == "omitted-zero" {
					raw = []byte(strings.Replace(string(raw), `"nextOffset":0,`, "", 1))
				}
				_, _ = w.Write(raw)
			}, nil)
			if _, err := c.Operations.GetReselection(t.Context(), reselectionTestOperation, reselectionTestAttempt); err == nil || errors.Is(err, ErrAmbiguous) {
				t.Fatal("invalid read response accepted or classified as mutation", err)
			}
		})
	}
}

func TestReselectionRedirectAndSourceFormatting(t *testing.T) {
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
	defer target.Close()
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}, nil)
	part := ReselectionSourcePart{Source: 1, End: true, Data: []byte("PRIVATE-PATH-AND-SOURCE")}
	if _, err := c.Operations.UploadReselectionSource(t.Context(), reselectionTestOperation, reselectionTestAttempt, part); err == nil || forwarded.Load() {
		t.Fatal("source followed redirect", err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", part, &part, part), "PRIVATE") {
		t.Fatal("source part formatted private bytes")
	}
	if _, err := json.Marshal(part); err == nil {
		t.Fatal("source part serialized outside binary method")
	}
}

func TestReselectionAsyncAdmissionDisposition(t *testing.T) {
	for _, operation := range []string{"VerifyCollectionReselection", "ResumeCollectionReselection"} {
		for _, phase := range []api.CollectionReselectionAttemptPhase{"uploading", "verifying", "verified", "transferring", "completed", "failed"} {
			t.Run(operation+"/"+string(phase), func(t *testing.T) {
				want := reselectionTestObservation()
				want.Phase, want.SourcesCompleted, want.NextSource = phase, want.SourceCount, 0
				if phase == "failed" {
					want.ErrorCode = api.Pointer(api.CollectionReselectionAttemptErrorCode("operation_changed"))
				}
				c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(want)
				}, nil)
				status, id, err := invokeReselection(t, c, operation)
				accepted := phase != "uploading"
				if operation == "ResumeCollectionReselection" {
					accepted = phase == "transferring" || phase == "completed" || phase == "failed"
				}
				if accepted {
					if err != nil || status != http.StatusAccepted || id != reselectionTestOperation {
						t.Fatal("admitted disposition rejected", err)
					}
				} else {
					var ambiguous *AmbiguousError
					if !errors.As(err, &ambiguous) || ambiguous.OperationID != reselectionTestOperation || id != reselectionTestOperation {
						t.Fatal("stale success did not preserve ambiguous original operation", err)
					}
				}
			})
		}
	}
}

func TestReselectionUploadResponseCoversSubmittedPart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		part     ReselectionSourcePart
		change   func(*api.CollectionReselectionAttempt)
		accepted bool
	}{
		{"end-stale", ReselectionSourcePart{Source: 2, Offset: 7, End: true, Data: []byte("abc")}, func(*api.CollectionReselectionAttempt) {}, false},
		{"part-stale", ReselectionSourcePart{Source: 2, Offset: 7, Data: []byte("abc")}, func(a *api.CollectionReselectionAttempt) { a.NextOffset = 9 }, false},
		{"source-outside-count", ReselectionSourcePart{Source: 4, End: true}, func(*api.CollectionReselectionAttempt) {}, false},
		{"bytes-not-covered", ReselectionSourcePart{Source: 1, End: true, Data: make([]byte, 18)}, func(*api.CollectionReselectionAttempt) {}, false},
		{"exact-part", ReselectionSourcePart{Source: 2, Offset: 7, Data: []byte("abc")}, func(a *api.CollectionReselectionAttempt) { a.NextOffset = 10 }, true},
		{"advanced-part", ReselectionSourcePart{Source: 2, Offset: 7, Data: []byte("abc")}, func(a *api.CollectionReselectionAttempt) { a.NextOffset = 15 }, true},
		{"completed-source", ReselectionSourcePart{Source: 1, Offset: 7, End: true, Data: []byte("abc")}, func(*api.CollectionReselectionAttempt) {}, true},
		{"later-source", ReselectionSourcePart{Source: 1, Offset: 7, Data: []byte("abc")}, func(*api.CollectionReselectionAttempt) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				a := reselectionTestObservation()
				tc.change(&a)
				_ = json.NewEncoder(w).Encode(a)
			}, nil)
			response, err := c.Operations.UploadReselectionSource(t.Context(), reselectionTestOperation, reselectionTestAttempt, tc.part)
			if tc.accepted {
				if err != nil {
					t.Fatal("covered source progress rejected", err)
				}
			} else {
				var ambiguous *AmbiguousError
				if !errors.As(err, &ambiguous) || ambiguous.OperationID != reselectionTestOperation || response.OperationID != reselectionTestOperation {
					t.Fatal("uncovered source progress accepted", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("mutation retried")
			}
		})
	}
}
