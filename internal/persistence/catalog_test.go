package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func openCatalogMemory(t *testing.T) *Store {
	t.Helper()
	c := testConfig(t)
	c.Storage.Mode = "memory"
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func catalogSealer(t *testing.T) *secureconfig.Sealer {
	t.Helper()
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

func catalogRecord(t *testing.T, s *Store, kind, id, uid, revision, plaintext string, refs ...CatalogKey) CatalogRecord {
	t.Helper()
	at := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	r := CatalogRecord{Key: CatalogKey{Kind: kind, ID: id}, UID: uid, Revision: revision,
		Generation: 1, Purpose: "resource", CreatedAt: at, UpdatedAt: at, References: refs}
	var err error
	r.Payload, err = catalogSealer(t).Seal(context.Background(), r.Binding(s.nodeID), []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func catalogConditions(t *testing.T, s *Store, refs []CatalogKey) []CatalogCondition {
	t.Helper()
	conditions := make([]CatalogCondition, 0, len(refs))
	for _, key := range refs {
		r := requireCatalog(t, s, key)
		conditions = append(conditions, CatalogCondition{Key: key, UID: r.UID, Revision: r.Revision})
	}
	return conditions
}

func catalogSubmit(t *testing.T, s *Store, mutation CatalogMutation) Result {
	t.Helper()
	results := submit(t, s, Command{Kind: "catalog", At: mutation.Record.UpdatedAt, Catalog: &mutation})
	if len(results) != 1 {
		t.Fatalf("expected one catalog result, got %d", len(results))
	}
	return results[0]
}

func createCatalog(t *testing.T, s *Store, r CatalogRecord) CatalogRecord {
	t.Helper()
	result := catalogSubmit(t, s, CatalogMutation{Record: r, Create: true, Conditions: catalogConditions(t, s, r.References)})
	if result.Err != nil || !result.Allowed || result.Catalog == nil {
		t.Fatalf("catalog create failed: %v", result.Err)
	}
	return *result.Catalog
}

func requireCatalog(t *testing.T, s *Store, key CatalogKey) CatalogRecord {
	t.Helper()
	r, ok, err := s.CatalogGet(key)
	if err != nil || !ok {
		t.Fatalf("catalog resource unavailable: %v, present=%v", err, ok)
	}
	return r
}

func updateCatalogMutation(t *testing.T, s *Store, old CatalogRecord, revision, plaintext string, refs ...CatalogKey) CatalogMutation {
	t.Helper()
	r := old.Clone()
	r.Revision, r.CommittedIndex, r.DependentsVersion = revision, 0, 0
	r.Generation++
	r.UpdatedAt = old.UpdatedAt.Add(time.Second)
	r.References = refs
	var err error
	r.Payload, err = catalogSealer(t).Seal(context.Background(), r.Binding(s.nodeID), []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return CatalogMutation{Record: r, ExpectedUID: old.UID, ExpectedRevision: old.Revision,
		ExpectedDependentsVersion: old.DependentsVersion, Conditions: catalogConditions(t, s, refs)}
}

func deleteCatalogMutation(old CatalogRecord, revision string) CatalogMutation {
	r := old.Clone()
	r.Revision, r.CommittedIndex, r.DependentsVersion = revision, 0, 0
	r.UpdatedAt = r.UpdatedAt.Add(time.Second)
	r.Removed, r.Payload, r.References = true, secureconfig.Envelope{}, nil
	return CatalogMutation{Record: r, ExpectedUID: old.UID, ExpectedRevision: old.Revision,
		ExpectedDependentsVersion: old.DependentsVersion}
}

func TestCatalogEncryptedSnapshotAndLogRestart(t *testing.T) {
	c := testConfig(t)
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const before = "catalog-test-provider-password-before-snapshot-123"
	const after = "catalog-test-provider-password-after-snapshot-456"
	secret := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "secret-uid", "secret-v1", before))
	endpoint := createCatalog(t, s, catalogRecord(t, s, "Endpoint", "mail", "endpoint-uid", "endpoint-v1", "private-provider-destination", secret.Key))
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	secret = requireCatalog(t, s, secret.Key)
	changed := catalogSubmit(t, s, updateCatalogMutation(t, s, secret, "secret-v2", after))
	if changed.Err != nil {
		t.Fatal(changed.Err)
	}
	nodeID := s.nodeID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(c.Storage.Directory)
	if err != nil {
		t.Fatalf("complete encrypted store failed stopped backup validation: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(c.Storage.Directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, plaintext := range []string{before, after, "private-provider-destination"} {
			if bytes.Contains(data, []byte(plaintext)) {
				return errors.New("plaintext provider data appeared in durable files")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if s.nodeID != nodeID {
		t.Fatal("restart changed the encryption binding's node identity")
	}
	recovered := requireCatalog(t, s, secret.Key)
	plaintext, err := catalogSealer(t).Open(context.Background(), recovered.Binding(s.nodeID), recovered.Payload)
	if err != nil || string(plaintext) != after || recovered.Revision != "secret-v2" {
		t.Fatalf("snapshot plus later committed update was not restored: %v", err)
	}
	recoveredEndpoint := requireCatalog(t, s, endpoint.Key)
	if !reflect.DeepEqual(recoveredEndpoint, endpoint) {
		t.Fatal("endpoint snapshot generation changed during replay")
	}
	dependents, version, err := s.CatalogDependents(secret.Key, 100)
	if err != nil || !reflect.DeepEqual(dependents, []CatalogKey{endpoint.Key}) || version != secret.DependentsVersion {
		t.Fatalf("reverse index was not rebuilt correctly: %v", err)
	}
}

func TestCatalogCrashHelper(t *testing.T) {
	directory := os.Getenv("CPRA_CATALOG_CRASH_DIRECTORY")
	if directory == "" {
		return
	}
	c := testConfig(t)
	c.Storage.Directory = directory
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	initial := catalogRecord(t, s, "Credential", "restart", "secret-uid", "v1", "before snapshot")
	initial.CreatedAt = time.Now().UTC()
	initial.UpdatedAt = initial.CreatedAt
	created := catalogSubmit(t, s, operationMutation(CatalogMutation{Record: initial, Create: true}))
	if created.Err != nil || created.Operation == nil {
		t.Fatal("initial resource/receipt not committed", created.Err)
	}
	r := *created.Catalog
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	result := catalogSubmit(t, s, operationMutation(updateCatalogMutation(t, s, r, "v2", "committed before forced termination")))
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	fmt.Println("CATALOG_COMMITTED")
	// The parent kills this process, bypassing Close and any deferred cleanup.
	select {}
}

func TestCatalogCommittedUpdateSurvivesProcessKill(t *testing.T) {
	c := testConfig(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCatalogCrashHelper$")
	cmd.Env = append(os.Environ(), "CPRA_CATALOG_CRASH_DIRECTORY="+c.Storage.Directory)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	committed := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "CATALOG_COMMITTED" {
				committed <- true
				return
			}
		}
		committed <- false
	}()
	select {
	case ok := <-committed:
		if !ok {
			t.Fatal("catalog subprocess exited before its committed update")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("catalog subprocess did not commit within its startup budget")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	s, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := requireCatalog(t, s, CatalogKey{Kind: "Credential", ID: "restart"})
	plaintext, err := catalogSealer(t).Open(context.Background(), r.Binding(s.nodeID), r.Payload)
	if err != nil || r.Revision != "v2" || string(plaintext) != "committed before forced termination" {
		t.Fatalf("process kill lost or invalidated the committed encrypted update: %v", err)
	}
	if prior, err := s.Operation("v1"); err != nil || prior.State != "partial" || prior.Outcome != "superseded" {
		t.Fatal("forced termination lost terminal history receipt", err)
	}
	if pending, err := s.Operation("v2"); err != nil || pending.State != "committed" || pending.Actor != "team/oncall" {
		t.Fatal("restart lost pending receipt or falsely applied configuration", err)
	}
}

func TestCatalogPreconditionsDeletionAndRecreation(t *testing.T) {
	s := openCatalogMemory(t)
	secret := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "s1", "r1", "secret"))
	staleSecret := secret
	endpoint := createCatalog(t, s, catalogRecord(t, s, "Endpoint", "mail", "e1", "r1", "endpoint", secret.Key))
	if got := catalogSubmit(t, s, updateCatalogMutation(t, s, staleSecret, "r2", "rotated")); !errors.Is(got.Err, ErrCatalogConflict) {
		t.Fatalf("stale reverse-reference guard was admitted: %v", got.Err)
	}
	secret = requireCatalog(t, s, secret.Key)
	if got := catalogSubmit(t, s, deleteCatalogMutation(secret, "deleted")); !errors.Is(got.Err, ErrCatalogReferenced) {
		t.Fatalf("referenced credential was removed: %v", got.Err)
	}
	mutation := updateCatalogMutation(t, s, endpoint, "r2", "changed", secret.Key)
	mutation.Conditions[0].UID = "wrong-incarnation"
	if got := catalogSubmit(t, s, mutation); !errors.Is(got.Err, ErrCatalogDependency) {
		t.Fatalf("incorrect dependency incarnation was admitted: %v", got.Err)
	}
	mutation = updateCatalogMutation(t, s, endpoint, "r2", "detached")
	if got := catalogSubmit(t, s, mutation); got.Err != nil {
		t.Fatal(got.Err)
	}
	if got := catalogSubmit(t, s, mutation); !errors.Is(got.Err, ErrCatalogConflict) {
		t.Fatalf("replayed stale mutation was admitted twice: %v", got.Err)
	}
	secret = requireCatalog(t, s, secret.Key)
	if got := catalogSubmit(t, s, deleteCatalogMutation(secret, "deleted")); got.Err != nil {
		t.Fatal(got.Err)
	}
	if _, ok, err := s.CatalogGet(secret.Key); err != nil || ok {
		t.Fatal("tombstone remained an active resource")
	}
	staleCreate := catalogRecord(t, s, "Credential", "shared", "s1", "new-version", "replacement")
	if got := catalogSubmit(t, s, CatalogMutation{Record: staleCreate, Create: true}); !errors.Is(got.Err, ErrCatalogConflict) {
		t.Fatalf("delete/recreate reused the previous incarnation: %v", got.Err)
	}
	replacement := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "s2", "new-version", "replacement"))
	if replacement.UID == secret.UID || replacement.DependentsVersion != 0 {
		t.Fatal("replacement inherited the deleted incarnation or incoming edges")
	}
	if got := catalogSubmit(t, s, updateCatalogMutation(t, s, secret, "old-client-edit", "wrong")); !errors.Is(got.Err, ErrCatalogConflict) {
		t.Fatalf("old client updated recreated credential: %v", got.Err)
	}
}

func TestCatalogRecursiveDependentCondition(t *testing.T) {
	s := openCatalogMemory(t)
	secret := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "s1", "r1", "secret"))
	group := createCatalog(t, s, catalogRecord(t, s, "Group", "team", "g1", "r1", "group", secret.Key))
	endpoint := createCatalog(t, s, catalogRecord(t, s, "Endpoint", "mail", "e1", "r1", "endpoint", group.Key))
	createCatalog(t, s, catalogRecord(t, s, "Monitor", "first", "m1", "r1", "monitor", endpoint.Key))
	secret = requireCatalog(t, s, secret.Key)
	endpoint = requireCatalog(t, s, endpoint.Key)
	mutation := updateCatalogMutation(t, s, secret, "r2", "rotated")
	guard := endpoint.DependentsVersion
	mutation.Conditions = []CatalogCondition{{Key: endpoint.Key, UID: endpoint.UID, Revision: endpoint.Revision, ExpectedDependentsVersion: &guard}}
	clone := mutation.Clone()
	*clone.Conditions[0].ExpectedDependentsVersion++
	clone.Record.Payload.Ciphertext[0] ^= 1
	if *mutation.Conditions[0].ExpectedDependentsVersion != guard || bytes.Equal(clone.Record.Payload.Ciphertext, mutation.Record.Payload.Ciphertext) {
		t.Fatal("mutation clone aliases its encrypted bytes or dependent condition")
	}
	createCatalog(t, s, catalogRecord(t, s, "Monitor", "second", "m2", "r1", "monitor", endpoint.Key))
	if current := requireCatalog(t, s, secret.Key); current.DependentsVersion != secret.DependentsVersion {
		t.Fatal("test requires a change below the immediate reverse reference")
	}
	if got := catalogSubmit(t, s, mutation); !errors.Is(got.Err, ErrCatalogDependency) {
		t.Fatalf("stale intermediate reverse-graph read was admitted: %v", got.Err)
	}
	current := requireCatalog(t, s, endpoint.Key)
	if current.Revision != endpoint.Revision || current.DependentsVersion == guard {
		t.Fatal("reverse changes must preserve resource revision but invalidate dependency guards")
	}
	if _, _, err := s.CatalogDependents(endpoint.Key, 1); err == nil {
		t.Fatal("bounded dependent validation silently truncated the affected monitors")
	}
	guard = current.DependentsVersion
	if got := catalogSubmit(t, s, mutation); got.Err != nil {
		t.Fatalf("fresh recursive dependency guard rejected: %v", got.Err)
	}
}

func TestCatalogViewsPagesAndSnapshotOwnTheirGenerations(t *testing.T) {
	s := openCatalogMemory(t)
	secret := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "s1", "r1", "secret"))
	for _, id := range []string{"charlie", "alpha", "bravo"} {
		createCatalog(t, s, catalogRecord(t, s, "Monitor", id, id+"-uid", "r1", id, secret.Key))
	}
	view, err := s.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if view.Len() != 4 || (CatalogView{}).Len() != 0 {
		t.Fatal("catalog view does not count all active kinds")
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	first, next, err := view.Page("Monitor", "", 2)
	if err != nil || len(first) != 2 || first[0].Key.ID != "alpha" || first[1].Key.ID != "bravo" || next != "bravo" {
		t.Fatalf("unstable first page: %v, %q, %v", first, next, err)
	}
	original := first[0].Clone()
	first[0].Payload.Ciphertext[0] ^= 1
	first[0].References[0].ID = "corrupted-return-value"
	if got, _ := view.Get(original.Key); !reflect.DeepEqual(got, original) {
		t.Fatal("returned page mutated its immutable generation")
	}
	returned := requireCatalog(t, s, original.Key)
	returned.Payload.Nonce[0] ^= 1
	returned.References[0].ID = "corrupted-read"
	if got := requireCatalog(t, s, original.Key); !reflect.DeepEqual(got, original) {
		t.Fatal("CatalogGet exposed mutable state")
	}
	mutation := updateCatalogMutation(t, s, original, "r2", "changed", secret.Key)
	if result := catalogSubmit(t, s, mutation); result.Err != nil {
		t.Fatal(result.Err)
	} else {
		result.Catalog.Payload.Ciphertext[0] ^= 1
	}
	bravo := requireCatalog(t, s, CatalogKey{Kind: "Monitor", ID: "bravo"})
	if result := catalogSubmit(t, s, deleteCatalogMutation(bravo, "deleted")); result.Err != nil {
		t.Fatal(result.Err)
	}
	createCatalog(t, s, catalogRecord(t, s, "Monitor", "delta", "d1", "r1", "new", secret.Key))
	second, final, err := view.Page("Monitor", next, 2)
	if err != nil || len(second) != 1 || second[0].Key.ID != "charlie" || final != "" {
		t.Fatalf("concurrent insertion/deletion changed the retained page: %v", err)
	}
	if got, _ := view.Get(original.Key); !reflect.DeepEqual(got, original) {
		t.Fatal("live update mutated an existing view")
	}
	imageBytes, err := json.Marshal(snapshot.(*frozenSnapshot).image)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := decodeImage(bytes.NewReader(imageBytes))
	if err != nil || !reflect.DeepEqual(frozen.Catalog[original.Key.indexKey()], original) {
		t.Fatalf("background snapshot changed with live catalog writes: %v", err)
	}
	for _, limit := range []int{-1, 0, 501} {
		if _, _, err := view.Page("Monitor", "", limit); err == nil {
			t.Fatalf("invalid page limit %d accepted", limit)
		}
	}
	// Read the retained generation concurrently with copy-on-write tree updates.
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				page, _, err := view.Page("Monitor", "", 100)
				if err != nil || len(page) != 3 || page[0].Revision != "r1" {
					t.Error("retained generation changed under concurrent reads")
					return
				}
			}
		}()
	}
	for i := range 5 {
		createCatalog(t, s, catalogRecord(t, s, "Monitor", fmt.Sprintf("new-%d", i), fmt.Sprintf("n%d", i), "r1", "new", secret.Key))
	}
	readers.Wait()
	if view.Len() != 4 {
		t.Fatal("retained view length changed with live mutation")
	}
}

func TestCatalogCorruptAndIncompatibleFormatsFailClosed(t *testing.T) {
	s := openCatalogMemory(t)
	r := createCatalog(t, s, catalogRecord(t, s, "Credential", "shared", "s1", "r1", "secret"))
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	data, err := json.Marshal(snapshot.(*frozenSnapshot).image)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*image){
		"unknown version":     func(i *image) { i.Version = 999 },
		"legacy with catalog": func(i *image) { i.Version = FormatVersion },
		"index mismatch":      func(i *image) { i.Catalog["Credential\x00wrong"] = i.Catalog[r.Key.indexKey()] },
		"future position": func(i *image) {
			v := i.Catalog[r.Key.indexKey()]
			v.CommittedIndex = i.Index + 1
			i.Catalog[r.Key.indexKey()] = v
		},
		"missing dependency": func(i *image) {
			v := i.Catalog[r.Key.indexKey()]
			v.References = []CatalogKey{{Kind: "Group", ID: "absent"}}
			i.Catalog[r.Key.indexKey()] = v
		},
		"payload format": func(i *image) {
			v := i.Catalog[r.Key.indexKey()]
			v.Payload.Format = 999
			i.Catalog[r.Key.indexKey()] = v
		},
		"tombstone payload": func(i *image) { v := i.Catalog[r.Key.indexKey()]; v.Removed = true; i.Catalog[r.Key.indexKey()] = v },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var i image
			if err := json.Unmarshal(data, &i); err != nil {
				t.Fatal(err)
			}
			mutate(&i)
			bad, err := json.Marshal(i)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeImage(bytes.NewReader(bad)); err == nil {
				t.Fatal("corrupt or incompatible catalog snapshot was accepted")
			}
		})
	}
	mutation := updateCatalogMutation(t, s, r, "r2", "new secret")
	valid := Command{Kind: "catalog", At: mutation.Record.UpdatedAt, Catalog: &mutation}
	for name, mutate := range map[string]func(*Command){
		"outcome":          func(c *Command) { c.Outcome = "plaintext-provider-secret" },
		"config":           func(c *Command) { c.Config = &Monitor{Name: "plaintext-provider-secret"} },
		"monitor identity": func(c *Command) { c.MonitorID = "unexpected" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if _, err := s.Submit(context.Background(), []Command{bad}); err == nil || strings.Contains(err.Error(), "plaintext-provider-secret") {
				t.Fatalf("plaintext catalog side-channel was not rejected safely: %v", err)
			}
		})
	}
	good, err := json.Marshal(envelope{Version: CatalogFormatVersion, Commands: []Command{valid}})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(envelope{Version: FormatVersion, Commands: []Command{valid}})
	for name, corrupt := range map[string][]byte{
		"legacy catalog":         legacy,
		"unknown envelope field": append([]byte(`{"unrecognized_guard":true,`), good[1:]...),
		"trailing data":          append(bytes.Clone(good), []byte(`{}`)...),
		"future version":         bytes.Replace(good, []byte(`"version":2`), []byte(`"version":999`), 1),
		"empty commands":         []byte(`{"version":2,"commands":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			f := &machine{}
			if _, ok := f.Apply(&raft.Log{Index: 1, Data: corrupt}).(error); !ok || f.err == nil {
				t.Fatal("replay accepted an incompatible or ambiguous command envelope")
			}
		})
	}
	// The FSM has no decryption keys. Structural validation accepts ciphertext;
	// activation must authenticate it with the complete identity before use.
	r.Payload.Ciphertext[0] ^= 1
	if _, err := catalogSealer(t).Open(context.Background(), r.Binding(s.nodeID), r.Payload); err == nil {
		t.Fatal("tampered committed ciphertext authenticated")
	}
}
