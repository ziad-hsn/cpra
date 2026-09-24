package collection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func freezeProfileTest(t *testing.T, input string) *Frozen {
	t.Helper()
	f, err := FreezeProfile(context.Background(), []Source{Reader("source", strings.NewReader(input))}, FileNormalizationProfile, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestFreezeProfileFixedCommitments(t *testing.T) {
	raw, err := os.ReadFile("testdata/file-base-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Profile string
		Cases   []struct {
			Name, Source, SourceBase64 string
			Valid                      bool
			Items                      []struct {
				ID             string
				Document, Item int
				JSON           string
			}
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			source := []byte(fixture.Source)
			if fixture.SourceBase64 != "" {
				source, err = base64.StdEncoding.DecodeString(fixture.SourceBase64)
				if err != nil {
					t.Fatal(err)
				}
			}
			parent := t.TempDir()
			f, err := FreezeProfile(context.Background(), []Source{Reader("source", bytes.NewReader(source))}, fixtures.Profile, Options{TempDir: parent})
			if (err == nil) != fixture.Valid {
				t.Fatal("fixed profile acceptance changed", err)
			}
			if !fixture.Valid {
				if f != nil {
					t.Fatal("failed profile returned a frozen inventory")
				}
				entries, readErr := os.ReadDir(parent)
				if readErr != nil || len(entries) != 0 {
					t.Fatal("failed profile retained staging", readErr)
				}
				return
			}
			defer f.Close()
			if f.Len() != len(fixture.Items) || f.NormalizationProfile() != FileNormalizationProfile {
				t.Fatal("profile/count changed")
			}
			req, err := f.creationRequest()
			if err != nil || req.NormalizationProfile != FileNormalizationProfile {
				t.Fatal("creation lost profile", err)
			}
			key, _ := hex.DecodeString(*req.IdentityKey)
			defer clear(key)
			sourceMAC, err := commitment.NewSourceAccumulator(key, 1, 64<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer sourceMAC.Close()
			token, _ := commitment.SourceToken(1)
			if err = sourceMAC.Begin(token); err != nil {
				t.Fatal(err)
			}
			if _, err = sourceMAC.Write(source); err != nil {
				t.Fatal(err)
			}
			if err = sourceMAC.End(); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := sourceMAC.Finish()
			if err != nil || hex.EncodeToString(fingerprint[:]) != *req.SourceFingerprint {
				t.Fatal("raw source commitment differs", err)
			}
			accumulated, err := commitment.NewAccumulator(key, uint64(len(fixture.Items)), fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			defer accumulated.Close()
			for i, expected := range fixture.Items {
				item, err := f.Item(context.Background(), i)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := json.Marshal(item.Resource)
				if err != nil || string(actual) != expected.JSON {
					t.Fatal("exact normalized bytes changed", err)
				}
				position := commitment.Position{Ordinal: uint64(i + 1), ID: expected.ID, Source: commitment.SourcePosition{Token: token, Document: uint64(expected.Document), Item: uint64(expected.Item)}}
				if item.Position != position || item.ID != expected.ID || item.Location.Document != expected.Document || item.Location.Item != expected.Item {
					t.Fatal("normalizer coordinates changed")
				}
				mac, err := commitment.ItemMAC(key, position, []byte(expected.JSON))
				if err != nil || hex.EncodeToString(mac[:]) != item.ContentDigest {
					t.Fatal("golden commitment differs", err)
				}
				if err = accumulated.Add(position, mac); err != nil {
					t.Fatal(err)
				}
				// The actual wire envelope must preserve the same literal golden bytes.
				wire, _ := json.Marshal(applyItem(item))
				var decoded struct{ Resource json.RawMessage }
				_ = json.Unmarshal(wire, &decoded)
				if string(decoded.Resource) != expected.JSON {
					t.Fatal("wire re-encoded normalization output")
				}
			}
			digest, err := accumulated.Finish()
			if err != nil || hex.EncodeToString(digest[:]) != f.Digest() {
				t.Fatal("golden collection commitment differs", err)
			}
		})
	}
}

func TestFreezeProfileOrderedSourcesAndChangedFiles(t *testing.T) {
	directory := t.TempDir()
	a, b := filepath.Join(directory, "a.json"), filepath.Join(directory, "b.json")
	for path, content := range map[string]string{a: monitor("a"), b: monitor("b")} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw := []string{monitor("b"), monitor("a"), "# retained comment\n", "", monitor("tail")}
	sources := []Source{File(b), File(directory), Reader("comment", strings.NewReader(raw[2])), Reader("empty", strings.NewReader(raw[3])), Reader("tail", strings.NewReader(raw[4]))}
	f, err := FreezeProfile(context.Background(), sources, FileNormalizationProfile, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	req, _ := f.creationRequest()
	key, _ := hex.DecodeString(*req.IdentityKey)
	defer clear(key)
	accumulator, _ := commitment.NewSourceAccumulator(key, 5, 64<<20)
	defer accumulator.Close()
	for i, input := range raw {
		token, _ := commitment.SourceToken(uint64(i + 1))
		if err = accumulator.Begin(token); err != nil {
			t.Fatal(err)
		}
		if _, err = accumulator.Write([]byte(input)); err != nil {
			t.Fatal(err)
		}
		if err = accumulator.End(); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := accumulator.Finish()
	if err != nil || hex.EncodeToString(digest[:]) != *req.SourceFingerprint {
		t.Fatal("expanded source order/empty source identity differs", err)
	}
	for i, id := range []string{"b", "a", "tail"} {
		item, err := f.Item(context.Background(), i)
		if err != nil || item.Resource.Metadata.ID != id {
			t.Fatal("explicit/lexical order changed", err)
		}
	}
	tail, _ := f.Item(context.Background(), 2)
	token, _ := commitment.SourceToken(5)
	if tail.Position.Source.Token != token {
		t.Fatal("empty sources disappeared from positions")
	}
	if err = os.WriteFile(a, []byte("now invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if item, err := f.Item(context.Background(), 1); err != nil || item.Resource.Metadata.ID != "a" {
		t.Fatal("original frozen bytes reread a changed source", err)
	}
	if updated, err := FreezeProfile(context.Background(), []Source{File(directory)}, FileNormalizationProfile, Options{}); err == nil || updated != nil {
		t.Fatal("fresh freeze ignored changed input")
	}
	// Comments and empty-file boundaries affect the commitment even when no
	// resource differs. Compare under the original key, not random new keys.
	changed, _ := commitment.NewSourceAccumulator(key, 5, 64<<20)
	defer changed.Close()
	for i, input := range raw {
		if i == 2 {
			input = "# edited comment\n"
		}
		token, _ := commitment.SourceToken(uint64(i + 1))
		_ = changed.Begin(token)
		_, _ = changed.Write([]byte(input))
		_ = changed.End()
	}
	other, _ := changed.Finish()
	if other == digest {
		t.Fatal("changed comments retained original source commitment")
	}
}

type profileCountReader struct {
	reads int
	r     io.Reader
}

func (r *profileCountReader) Read(p []byte) (int, error) { r.reads++; return r.r.Read(p) }

func TestFreezeProfileBoundsBeforeReadingAndCleanup(t *testing.T) {
	for _, profile := range []string{"", "cpra.file.base.v0", "external"} {
		r := &profileCountReader{r: strings.NewReader(monitor("one"))}
		if f, err := FreezeProfile(context.Background(), []Source{Reader("source", r)}, profile, Options{}); !errors.Is(err, ErrUnsupportedNormalization) || f != nil || r.reads != 0 {
			t.Fatal("unknown profile read source", err)
		}
	}
	for _, options := range []Options{{MaxStagingBytes: (512 << 20) + 1}, {MaxSourceBytes: (64 << 20) + 1}, {MaxResources: 10001}, {MaxResourceBytes: (1 << 20) + 1}, {MaxDocumentBytes: (16 << 20) + 1}, {MaxSourceBytes: -1}, {MaxStagingBytes: -1}, {MaxResources: -1}} {
		r := &profileCountReader{r: strings.NewReader(monitor("one"))}
		if f, err := FreezeProfile(context.Background(), []Source{Reader("source", r)}, FileNormalizationProfile, options); err == nil || f != nil || r.reads != 0 {
			t.Fatal("invalid options read source", err)
		}
	}
	for _, name := range []string{"late-invalid", "duplicate", "utf8", "raw-quota", "count", "staging", "reader-error", "canceled"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			options := Options{TempDir: parent}
			ctx := context.Background()
			sources := []Source{Reader("first", strings.NewReader(monitor("one"))), Reader("last", strings.NewReader(monitor("two")))}
			switch name {
			case "late-invalid":
				sources[1] = Reader("last", strings.NewReader(`{"kind":`))
			case "duplicate":
				sources[1] = Reader("last", strings.NewReader(monitor("one")))
			case "utf8":
				sources[1] = Reader("last", bytes.NewReader([]byte{0xff}))
			case "raw-quota":
				options.MaxSourceBytes = int64(len(monitor("one")) + len(monitor("two")) - 1)
			case "count":
				options.MaxResources = 1
			case "staging":
				options.MaxStagingBytes = 1
			case "reader-error":
				sources[1] = Reader("last", profileErrorReader{})
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			f, err := FreezeProfile(ctx, sources, FileNormalizationProfile, options)
			if err == nil || f != nil || strings.Contains(err.Error(), "PRIVATE-SOURCE-ERROR") {
				t.Fatal("failed input accepted or source error leaked", err)
			}
			files, err := os.ReadDir(parent)
			if err != nil || len(files) != 0 {
				t.Fatal("failed freeze kept plaintext staging", err)
			}
		})
	}
	// The raw limit is cumulative; empty final sources still complete normally
	// when the preceding sources have consumed exactly the byte quota.
	input := monitor("one")
	f, err := FreezeProfile(context.Background(), []Source{Reader("one", strings.NewReader(input)), Reader("empty", strings.NewReader(""))}, FileNormalizationProfile, Options{MaxSourceBytes: int64(len(input))})
	if err != nil {
		t.Fatal("exact cumulative raw boundary rejected", err)
	}
	size := f.StagedBytes()
	_ = f.Close()
	for _, limit := range []int64{size - 1, size} {
		f, err = FreezeProfile(context.Background(), []Source{Reader("one", strings.NewReader(input))}, FileNormalizationProfile, Options{MaxStagingBytes: limit})
		if (err == nil) != (limit == size) {
			t.Fatal("staging byte boundary changed", limit, size, err)
		}
		if f != nil {
			_ = f.Close()
		}
	}
}

type profileErrorReader struct{}

func (profileErrorReader) Read([]byte) (int, error) { return 0, errors.New("PRIVATE-SOURCE-ERROR") }

func TestFreezeProfileExpandedSourceLimitAndUnprofiledInputs(t *testing.T) {
	for _, count := range []int{1000, 1001} {
		sources := make([]Source, count)
		for i := range sources {
			sources[i] = Reader("empty", strings.NewReader(""))
		}
		f, err := FreezeProfile(context.Background(), sources, FileNormalizationProfile, Options{})
		if (err == nil) != (count == 1000) {
			t.Fatal("expanded source bound changed", err)
		}
		if f != nil {
			if f.Len() != 0 || f.NormalizationProfile() != FileNormalizationProfile {
				t.Fatal("empty profile changed")
			}
			_ = f.Close()
		}
	}
	dir := t.TempDir()
	for i := 0; i < 1001; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.json", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if f, err := FreezeProfile(context.Background(), []Source{File(dir)}, FileNormalizationProfile, Options{}); err == nil || f != nil {
		t.Fatal("directory bypassed expanded source bound")
	}
	plain, err := Freeze(context.Background(), []Source{File(dir)}, Options{})
	if err != nil {
		t.Fatal("ordinary source limits changed", err)
	}
	defer plain.Close()
	if plain.NormalizationProfile() != "" {
		t.Fatal("ordinary Freeze acquired profile")
	}
	r, err := api.DecodeResource([]byte(monitor("typed")))
	if err != nil {
		t.Fatal(err)
	}
	typed, err := FreezeResources(context.Background(), Slice([]api.Resource{r}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer typed.Close()
	if req, _ := typed.creationRequest(); req.NormalizationProfile != "" || typed.NormalizationProfile() != "" {
		t.Fatal("typed Freeze acquired profile")
	}
}

type profileOperations struct {
	*fakeOperations
	prepareRequest api.CollectionPrepareRequest
	createRequest  api.OperationCreateRequest
	alter          string
	replacement    string
}

func (o *profileOperations) Prepare(ctx context.Context, r api.CollectionPrepareRequest) (*cpra.Response[api.CollectionAdmission], error) {
	o.prepareRequest = r
	return o.fakeOperations.Prepare(ctx, r)
}
func (o *profileOperations) Create(ctx context.Context, r api.OperationCreateRequest) (*cpra.Response[api.Operation], error) {
	o.createRequest = r
	reply, err := o.fakeOperations.Create(ctx, r)
	o.operation.NormalizationProfile = r.NormalizationProfile
	reply.Data = o.operation
	o.change("create", &reply.Data)
	return reply, err
}
func (o *profileOperations) Upload(ctx context.Context, id string, r api.UploadRequest) (*cpra.Response[api.Operation], error) {
	reply, err := o.fakeOperations.Upload(ctx, id, r)
	if reply != nil {
		o.change("upload", &reply.Data)
	}
	return reply, err
}
func (o *profileOperations) Validate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	reply, err := o.fakeOperations.Validate(ctx, id)
	o.change("validate", &reply.Data)
	return reply, err
}
func (o *profileOperations) Activate(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	reply, err := o.fakeOperations.Activate(ctx, id)
	o.change("activate", &reply.Data)
	return reply, err
}
func (o *profileOperations) Get(ctx context.Context, id string) (*cpra.Response[api.Operation], error) {
	reply, err := o.fakeOperations.Get(ctx, id)
	o.change("get", &reply.Data)
	return reply, err
}
func (o *profileOperations) change(phase string, r *api.Operation) {
	if o.alter == phase {
		r.NormalizationProfile = o.replacement
	}
}

func TestFreezeProfileApplyAndResume(t *testing.T) {
	f := freezeProfileTest(t, monitor("one"))
	backend := &profileOperations{fakeOperations: &fakeOperations{valid: true}}
	if result, err := Apply(context.Background(), backend, f); err != nil || result.Operation.NormalizationProfile != FileNormalizationProfile {
		t.Fatal("profile Apply failed", err)
	}
	if backend.prepareRequest.NormalizationProfile != FileNormalizationProfile || backend.createRequest.NormalizationProfile != FileNormalizationProfile {
		t.Fatal("creation did not bind immutable profile")
	}
	if _, err := Preflight(context.Background(), backend, f); err != nil {
		t.Fatal("exact-byte ephemeral preflight failed", err)
	}
	backend.operation.State = "uploading"
	backend.operation.Uploaded = api.Pointer(int64(0))
	backend.calls = nil
	if result, err := Resume(context.Background(), backend, "epoch.1", f); err != nil || result.Operation.NormalizationProfile != FileNormalizationProfile {
		t.Fatal("profile resume failed", err)
	}
	if !reflect.DeepEqual(backend.calls, []string{"get", "upload", "validate", "waitValidation", "activate"}) {
		t.Fatal("resume renewed creation", backend.calls)
	}
	for _, phase := range []string{"create", "upload", "validate", "activate", "get"} {
		for _, replacement := range []string{"", "cpra.file.future.v1"} {
			t.Run(phase+"/"+replacement, func(t *testing.T) {
				input := freezeProfileTest(t, monitor("one"))
				o := &profileOperations{fakeOperations: &fakeOperations{valid: true}, alter: phase, replacement: replacement}
				var result Result
				var err error
				if phase == "get" {
					o.operation = api.Operation{ID: "original", State: "uploading", IdentityFormat: commitment.Format, NormalizationProfile: FileNormalizationProfile, ContentDigest: input.Digest(), ItemCount: api.Pointer(int64(input.Len())), Uploaded: api.Pointer(int64(0))}
					result, err = Resume(context.Background(), o, "original", input)
				} else {
					result, err = Apply(context.Background(), o, input)
				}
				if err == nil || result.OperationID == "" || o.calls[len(o.calls)-1] != phase {
					t.Fatal("profile mismatch advanced original operation", err, o.calls)
				}
			})
		}
	}
	another := freezeProfileTest(t, monitor("changed"))
	backend.calls = nil
	if _, err := Resume(context.Background(), backend, "epoch.1", another); err == nil || !reflect.DeepEqual(backend.calls, []string{"get"}) {
		t.Fatal("changed frozen input resumed old identity", err)
	}
}

func TestFreezeProfilePrivateStagingAndClose(t *testing.T) {
	f := freezeProfileTest(t, `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"key"},"spec":{"value":"PRIVATE-PROFILE-VALUE"}}`)
	req, _ := f.creationRequest()
	for _, value := range []any{f, *f} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			text := fmt.Sprintf(format, value)
			if strings.Contains(text, "PRIVATE-PROFILE-VALUE") || strings.Contains(text, *req.IdentityKey) {
				t.Fatal("private frozen formatting leaked")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private frozen serialized")
		}
	}
	dir := f.dir
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Close retained staging", err)
	}
	if f.key != [commitment.KeyBytes]byte{} || f.sourceFingerprint != [commitment.MACBytes]byte{} {
		t.Fatal("Close retained raw identity secrets")
	}
	if f.NormalizationProfile() != FileNormalizationProfile {
		t.Fatal("Close rewrote public profile identity")
	}
	if _, err := f.Item(context.Background(), 0); err == nil {
		t.Fatal("closed profile remained readable")
	}
}
