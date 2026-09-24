package collection

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func TestFrozenIdentityBindsRawSourcesAndExactResourceBytes(t *testing.T) {
	// Insignificant JSON whitespace still belongs to the raw source commitment.
	inputs := []string{" \n" + monitor("first"), monitor("second") + "\n"}
	f, err := Freeze(context.Background(), []Source{Reader("private/team-a.json", strings.NewReader(inputs[0])), Reader("private/team-b.json", strings.NewReader(inputs[1]))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	request, err := f.creationRequest()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(*request.IdentityKey)
	defer clear(key)
	sources, err := commitment.NewSourceAccumulator(key, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	for i, raw := range inputs {
		token, _ := commitment.SourceToken(uint64(i + 1))
		if err := sources.Begin(token); err != nil {
			t.Fatal(err)
		}
		if _, err := sources.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := sources.End(); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := sources.Finish()
	if err != nil || hex.EncodeToString(fingerprint[:]) != *request.SourceFingerprint {
		t.Fatal("raw source commitment differs", err)
	}
	a, err := commitment.NewAccumulator(key, uint64(request.ItemCount), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := f.Range(context.Background(), func(item Item) error {
		raw, err := json.Marshal(item.Resource)
		if err != nil {
			return err
		}
		mac, err := hex.DecodeString(item.ContentDigest)
		if err != nil {
			return err
		}
		if err := commitment.VerifyItem(key, item.Position, raw, mac); err != nil {
			return err
		}
		wire, err := json.Marshal(applyItem(item))
		if err != nil {
			return err
		}
		if bytes.Contains(wire, []byte("private/")) || item.Position.Source.Token == item.Location.Source {
			t.Fatal("local source path escaped in upload")
		}
		var decoded struct{ Resource json.RawMessage }
		if err := json.Unmarshal(wire, &decoded); err != nil {
			return err
		}
		if !bytes.Equal(decoded.Resource, raw) {
			t.Fatal("frozen bytes changed in upload envelope")
		}
		return a.Add(item.Position, [commitment.MACBytes]byte(mac))
	}); err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString(request.ContentDigest)
	if err != nil || a.Verify(digest) != nil {
		t.Fatal("ordered inventory does not verify", err)
	}
}

func TestFrozenPrivateIdentityLifetimeAndNewFreeze(t *testing.T) {
	input := `{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"credential"},"spec":{"value":"fictional-private-input"}}`
	f := freezeTest(t, input)
	another := freezeTest(t, input)
	request, err := f.creationRequest()
	if err != nil {
		t.Fatal(err)
	}
	if f.Digest() == another.Digest() || bytes.Equal(f.key[:], another.key[:]) {
		t.Fatal("new Freeze reused a private identity")
	}
	stored, err := os.ReadFile(f.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, f.key[:]) || bytes.Contains(stored, []byte(*request.IdentityKey)) || bytes.Contains(stored, []byte(*request.SourceFingerprint)) {
		t.Fatal("private resume key was persisted in staging")
	}
	for _, value := range []any{f, *f} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			printed := fmt.Sprintf(format, value)
			if !strings.Contains(printed, "input omitted") || strings.Contains(printed, *request.IdentityKey) || strings.Contains(printed, "fictional-private-input") {
				t.Fatal("private Frozen formatting escaped")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private Frozen serialized")
		}
	}
	backend := &fakeOperations{operation: api.Operation{IdentityFormat: commitment.Format, ItemCount: api.Pointer(int64(1)), ID: "original", State: "staging", ContentDigest: f.Digest(), Uploaded: api.Pointer(int64(0))}}
	if _, err := Resume(context.Background(), backend, "original", another); err == nil || len(backend.calls) != 1 {
		t.Fatal("reselected identical input resumed an old operation", err)
	}
	dir := f.dir
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if f.key != [commitment.KeyBytes]byte{} || f.sourceFingerprint != [commitment.MACBytes]byte{} {
		t.Fatal("Close retained owned private buffers")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private staging retained after Close", err)
	}
	if _, err := f.creationRequest(); err == nil {
		t.Fatal("closed input yielded request identity")
	}
	if err := f.Close(); err != nil {
		t.Fatal("repeated Close", err)
	}
}

func TestFrozenReadbackRejectsTamperedResource(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	stored, err := os.ReadFile(f.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(stored, []byte("example.test"), []byte("changed.test"), 1)
	if bytes.Equal(stored, changed) || len(stored) != len(changed) {
		t.Fatal("invalid tamper fixture")
	}
	if _, err := f.file.WriteAt(changed, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Item(context.Background(), 0); err == nil {
		t.Fatal("changed staged resource passed identity verification")
	}
}

func TestFrozenCumulativeSourceQuotaAndCanceledEmpty(t *testing.T) {
	dir := t.TempDir()
	_, err := Freeze(context.Background(), []Source{Reader("one", strings.NewReader("    ")), Reader("two", strings.NewReader("    "))}, Options{TempDir: dir, MaxStagingBytes: 1024, MaxSourceBytes: 7})
	if err == nil {
		t.Fatal("cumulative raw bytes bypassed quota after releasing prior source")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("quota failure retained staging", err)
	}
	var resource api.Resource
	if err := json.Unmarshal([]byte(monitor("typed")), &resource); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resource)
	_, err = FreezeResources(context.Background(), Slice([]api.Resource{resource}), Options{TempDir: dir, MaxSourceBytes: int64(len(raw))})
	if err == nil {
		t.Fatal("typed source omitted LF from cumulative accounting")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Freeze(ctx, []Source{File("missing-input")}, Options{TempDir: dir}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Freeze touched input", err)
	}
	empty, err := Freeze(context.Background(), nil, Options{TempDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := Preflight(ctx, nil, empty); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled empty preflight reported success", err)
	}
}

func TestFrozenConcurrentReadCloseAndInvalidZeroValue(t *testing.T) {
	f := freezeTest(t, monitor("one"))
	var start sync.WaitGroup
	start.Add(8)
	for range 8 {
		go func() {
			defer start.Done()
			for range 20 {
				_, _ = f.Item(context.Background(), 0)
			}
		}()
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	start.Wait()
	var zero Frozen
	if _, err := Apply(context.Background(), &fakeOperations{}, &zero); err == nil {
		t.Fatal("uninitialized Frozen accepted")
	}
	if _, err := zero.Item(context.Background(), 0); err == nil {
		t.Fatal("uninitialized read accepted")
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}
