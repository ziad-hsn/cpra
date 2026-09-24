package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func monitor(id string) string {
	return fmt.Sprintf(`{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":%q},"spec":{"enabled":false,"check":{"driver":{"type":"http","config":{"url":"https://example.test","headers":{"X_Service":"keep_key"}}},"interval":"60s","timeout":"5s"}}}`, id)
}
func freezeTest(t *testing.T, text string) *Frozen {
	t.Helper()
	f, err := Freeze(context.Background(), []Source{Reader("test.yaml", strings.NewReader(text))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestFrozenBytesOrderingAndOverlap(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	if err := os.WriteFile(a, []byte(monitor("a")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(monitor("b")), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Freeze(context.Background(), []Source{File(dir), File(a)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Len() != 2 {
		t.Fatalf("got %d", f.Len())
	}
	if err := os.WriteFile(a, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := f.Item(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Resource.Metadata.ID != "a" {
		t.Fatal(first.Resource.Metadata)
	}
	var spec map[string]any
	if err := json.Unmarshal(first.Resource.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["enabled"] != false {
		t.Fatal("false lost")
	}
	if f.Digest() == "" {
		t.Fatal("missing content identity")
	}
}

func TestDuplicateIdenticalDefinitionsAndCleanup(t *testing.T) {
	stage := t.TempDir()
	_, err := Freeze(context.Background(), []Source{Reader("one", strings.NewReader(monitor("same"))), Reader("two", strings.NewReader(monitor("same")))}, Options{TempDir: stage})
	if err == nil || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("unexpected error %v", err)
	}
	files, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("failed collection left private staging")
	}
}

func TestYAMLJSONAndHugeManifestStreaming(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var text strings.Builder
			if format == "yaml" {
				text.WriteString("version: 1\nmonitors:\n")
			} else {
				text.WriteString(`{"version":1,"monitors":[`)
			}
			for i := 0; i < 300; i++ {
				if format == "yaml" {
					fmt.Fprintf(&text, "  - name: service-%d\n    enabled: false\n    pulse_check:\n      type: http\n      interval: 60s\n      timeout: 5s\n      config:\n        url: https://example.test\n", i)
				} else {
					if i > 0 {
						text.WriteString(",")
					}
					fmt.Fprintf(&text, `{"name":"service-%d","enabled":false,"pulse_check":{"type":"http","interval":"60s","timeout":"5s","config":{"url":"https://example.test"}}}`, i)
				}
			}
			if format == "json" {
				text.WriteString("]}")
			}
			f, err := Freeze(context.Background(), []Source{Reader("large."+format, strings.NewReader(text.String()))}, Options{MaxResourceBytes: 2048, MaxDocumentBytes: 2048})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Len() != 300 {
				t.Fatal(f.Len())
			}
			item, err := f.Item(context.Background(), 299)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(item.Resource.Metadata.ID, "name:") {
				t.Fatal("manifest identity changed")
			}
		})
	}
}

func TestSharedOnlyAndCrossFileReferences(t *testing.T) {
	shared := `endpoints:
  log:
    type: log
    config:
      file: /tmp/cpra-test.log
notification_groups:
  oncall: [log]
`
	f, err := Freeze(context.Background(), []Source{Reader("shared", strings.NewReader(shared))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	versions, err := ValidateReferences(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 || f.Len() != 2 {
		t.Fatal("shared resources not indexed")
	}
	group := `{"apiVersion":"cpra.io/v2","kind":"NotificationGroup","metadata":{"id":"oncall"},"spec":{"endpointRefs":["live"]}}`
	g := freezeTest(t, group)
	if _, err := ValidateReferences(context.Background(), g, nil); err == nil {
		t.Fatal("missing dependency accepted")
	}
	versions, err = ValidateReferences(context.Background(), g, func(ctx context.Context, r Reference) (string, error) {
		if r.Kind != "NotificationEndpoint" || r.ID != "live" {
			t.Fatal(r)
		}
		return "rv1", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if versions[Reference{"NotificationEndpoint", "live"}] != "rv1" {
		t.Fatal(versions)
	}
}

func TestListMultiDocumentDuplicateKeysAndLimits(t *testing.T) {
	text := "apiVersion: cpra.io/v2\nkind: List\nitems:\n  - " + monitor("a") + "\n  - " + monitor("b") + "\n---\n" + monitor("c") + "\n"
	f := freezeTest(t, text)
	if f.Len() != 3 {
		t.Fatal(f.Len())
	}
	for _, bad := range []string{`{"kind":"Monitor","kind":"Monitor"}`, "kind: Monitor\nkind: Monitor\n", "kind: List\nitems: &many [*many]\n", `{"monitors":[],"monitors":[]}`, `{"items":[],"kind":"List",}`} {
		if _, err := Freeze(context.Background(), []Source{Reader("bad", strings.NewReader(bad))}, Options{}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := Freeze(context.Background(), []Source{Reader("quota", strings.NewReader(monitor("quota")))}, Options{MaxStagingBytes: 32}); err == nil {
		t.Fatal("quota ignored")
	}
}

func TestSourceURLsAreSeparateBoundedAndRedacted(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/resource?signed=super-secret", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, monitor("url"))
	}))
	defer server.Close()
	if _, err := Freeze(context.Background(), []Source{URL(server.URL)}, Options{}); err == nil {
		t.Fatal("plain HTTP did not require opt-in")
	}
	f, err := Freeze(context.Background(), []Source{URL(server.URL + "/redirect?token=private")}, Options{AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	item, err := f.Item(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" || strings.Contains(item.Location.Source, "token") || strings.Contains(item.Location.Source, "private") {
		t.Fatal("source credential leaked")
	}
	badURL := strings.Replace(server.URL, "http://", "http://user:private@", 1)
	if _, err := Freeze(context.Background(), []Source{URL(badURL)}, Options{AllowHTTP: true}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("URL credentials: %v", err)
	}
}

func TestCancellationAndEmptyNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Freeze(ctx, []Source{Reader("cancel", strings.NewReader(monitor("a")))}, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f := freezeTest(t, "# only a comment\n---\n")
	result, err := Apply(context.Background(), nil, f)
	if err != nil || !result.Noop {
		t.Fatal(result, err)
	}
}

type fakeOperations struct {
	calls      []string
	operation  api.Operation
	valid      bool
	uploads    [][]api.ApplyItem
	failUpload error
}

func (f *fakeOperations) Preflight(_ context.Context, r api.PreflightRequest) (*cpra.Response[api.Preflight], error) {
	f.calls = append(f.calls, "preflight")
	return &cpra.Response[api.Preflight]{Data: api.Preflight{Valid: f.valid, ContentDigest: r.ContentDigest, IdentityFormat: commitment.Format, ItemCount: api.Pointer(r.ItemCount)}}, nil
}
func (f *fakeOperations) Prepare(_ context.Context, _ api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
	f.calls = append(f.calls, "prepare")
	return &cpra.Response[api.CollectionAdmission]{Data: api.CollectionAdmission{Ticket: "fixture-ticket", ExpiresAt: time.Now().Add(time.Hour)}}, nil
}
func (f *fakeOperations) Create(_ context.Context, r api.OperationCreateRequest) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "create")
	f.operation = api.Operation{ID: "epoch.1", ContentDigest: r.ContentDigest, IdentityFormat: commitment.Format, ItemCount: api.Pointer(r.ItemCount), State: "staging", Uploaded: api.Pointer(int64(0))}
	return &cpra.Response[api.Operation]{Data: f.operation}, nil
}
func (f *fakeOperations) Upload(_ context.Context, id string, r api.UploadRequest) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "upload")
	if f.failUpload != nil {
		return nil, f.failUpload
	}
	f.uploads = append(f.uploads, append([]api.ApplyItem(nil), r.Items...))
	return &cpra.Response[api.Operation]{Data: f.operation}, nil
}
func (f *fakeOperations) Validate(_ context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "validate")
	f.operation.State = "validating"
	f.operation.Uploaded = api.Pointer(*f.operation.ItemCount)
	return &cpra.Response[api.Operation]{Data: f.operation, StatusCode: 202}, nil
}
func (f *fakeOperations) WaitValidation(_ context.Context, id string) (*cpra.Response[api.ValidationResultPage], error) {
	f.calls = append(f.calls, "waitValidation")
	page := validationPageFixture(f.operation, f.valid)
	for _, chunk := range f.uploads {
		for _, item := range chunk {
			if item.Ordinal <= int64(len(page.Items)) {
				row := &page.Items[item.Ordinal-1]
				row.Kind = item.Resource.Kind
				row.ID = item.Resource.Metadata.ID
				row.Source = item.Source
				row.SourceDocument = item.SourceDocument
				row.SourceItem = item.SourceItem
			}
		}
	}
	return &cpra.Response[api.ValidationResultPage]{Data: page}, nil
}
func validationPageFixture(operation api.Operation, valid bool) api.ValidationResultPage {
	count := int64(0)
	if operation.ItemCount != nil {
		count = *operation.ItemCount
	}
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	summary := api.ValidationResultSummary{ResultID: "original-result", Valid: valid, Count: count, Digest: strings.Repeat("a", 64), CapabilitiesDigest: strings.Repeat("b", 64), FinalizedAt: at, ExpiresAt: at.Add(30 * 24 * time.Hour)}
	if valid {
		summary.PlanID = "original-plan"
		summary.PlanDigest = strings.Repeat("c", 64)
	} else {
		summary.Issue = "invalidGraph"
	}
	page := api.ValidationResultPage{OperationID: operation.ID, IdentityFormat: string(operation.IdentityFormat), ContentDigest: operation.ContentDigest, ItemCount: count, Summary: summary, Items: []api.ValidationResultItem{}}
	for i := int64(1); i <= min(count, 100); i++ {
		page.Items = append(page.Items, api.ValidationResultItem{Ordinal: i, Kind: "Monitor", ID: fmt.Sprint(i), Source: "source.00000000000000000001", SourceDocument: 1, SourceItem: i, Change: "create"})
	}
	if count > 100 {
		page.NextCursor = "more"
	}
	return page
}
func (f *fakeOperations) Activate(_ context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "activate")
	f.operation.State = "applying"
	return &cpra.Response[api.Operation]{Data: f.operation}, nil
}
func (f *fakeOperations) Get(_ context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "get")
	return &cpra.Response[api.Operation]{Data: f.operation}, nil
}
func (f *fakeOperations) Cancel(_ context.Context, id string) (*cpra.Response[api.Operation], error) {
	f.calls = append(f.calls, "cancel")
	return &cpra.Response[api.Operation]{Data: f.operation}, nil
}

func TestApplyFullValidationBeforeActivationAndResumeIdentity(t *testing.T) {
	f := freezeTest(t, monitor("a"))
	backend := &fakeOperations{}
	result, err := Apply(context.Background(), backend, f)
	if !errors.Is(err, ErrPreflightRejected) || result.OperationID != "epoch.1" {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(backend.calls, []string{"prepare", "create", "upload", "validate", "waitValidation"}) {
		t.Fatal(backend.calls)
	}
	f = freezeTest(t, monitor("a")) // A new frozen identity is an explicit new attempt.
	backend = &fakeOperations{valid: true}
	result, err = Apply(context.Background(), backend, f)
	if err != nil || result.Operation.State != "applying" {
		t.Fatal(result, err)
	}
	changed := freezeTest(t, monitor("b"))
	if _, err = Resume(context.Background(), backend, "epoch.1", changed); err == nil {
		t.Fatal("changed content resumed operation")
	}
}

func TestApplyChunksAndAmbiguousHandle(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("[")
	for i := 0; i < 600; i++ {
		if i > 0 {
			raw.WriteByte(',')
		}
		raw.WriteString(monitor(fmt.Sprint(i)))
	}
	raw.WriteByte(']')
	f := freezeTest(t, raw.String())
	backend := &fakeOperations{valid: true}
	if _, err := Apply(context.Background(), backend, f); err != nil {
		t.Fatal(err)
	}
	var total int
	for _, chunk := range backend.uploads {
		if len(chunk) > MaxChunkItems {
			t.Fatal("oversized chunk")
		}
		data, _ := json.Marshal(api.UploadRequest{Items: chunk})
		if len(data) > MaxChunkBytes {
			t.Fatal("oversized encoded request")
		}
		total += len(chunk)
	}
	if total != 600 || len(backend.uploads) != 3 {
		t.Fatalf("total=%d chunks=%d", total, len(backend.uploads))
	}
	backend = &fakeOperations{valid: true, failUpload: &cpra.AmbiguousError{}}
	result, err := Apply(context.Background(), backend, f)
	if !errors.Is(err, cpra.ErrAmbiguous) || result.OperationID == "" {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(backend.calls, []string{"create", "upload"}) {
		t.Fatal("ambiguous operation retried or cancelled", backend.calls)
	}
}

func TestMalformedLastFileNeverCreatesOperation(t *testing.T) {
	backend := &fakeOperations{valid: true}
	f, err := Freeze(context.Background(), []Source{Reader("good", strings.NewReader(monitor("a"))), Reader("late", strings.NewReader("monitors: [broken"))}, Options{})
	if err == nil {
		_, _ = Apply(context.Background(), backend, f)
		_ = f.Close()
		t.Fatal("invalid final source accepted")
	}
	if len(backend.calls) != 0 {
		t.Fatal("active/staged server call before full parse")
	}
}
