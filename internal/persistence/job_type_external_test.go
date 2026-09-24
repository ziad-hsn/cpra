//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func jobTypeFixture(t *testing.T, s *Store, id string) JobTypeCommand {
	t.Helper()
	if s.fsm.image.Authentication == nil {
		if _, err := s.CommitAuthentication(t.Context(), authenticationBootstrap()); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	authority, err := s.ObserveOperatorAuthority(t.Context(), "oncall", at)
	if err != nil {
		t.Fatal(err)
	}
	r := CatalogRecord{Key: CatalogKey{Kind: "JobType", ID: id}, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "job-type", CreatedAt: at, UpdatedAt: at}
	r.Payload, err = catalogSealer(t).Seal(t.Context(), r.Binding(s.nodeID), []byte("job-type-private-schema-canary"))
	if err != nil {
		t.Fatal(err)
	}
	return JobTypeCommand{Action: "create", Value: JobTypeVersion{Record: r, Version: "v1", Category: "check", Handler: "probe", ProtocolVersion: "1", SchemaProfile: "cpra.schema.v1"}, Authority: authority}
}

func jobTypeChange(t *testing.T, s *Store, state JobTypeState, version string) JobTypeCommand {
	t.Helper()
	v := state.Current.Clone()
	r := &v.Record
	uid, revision := r.UID, r.Revision
	r.CommittedIndex = 0
	r.Generation++
	r.Revision = uuid.NewString()
	r.UpdatedAt = r.UpdatedAt.Add(time.Second)
	v.Version = version
	var err error
	r.Payload, err = catalogSealer(t).Seal(t.Context(), r.Binding(s.nodeID), []byte("job-type-private-metadata-canary"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := s.ObserveOperatorAuthority(t.Context(), "oncall", r.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return JobTypeCommand{Action: "replace", Value: v, ExpectedUID: uid, ExpectedRevision: revision, Authority: authority}
}

func commitJobType(t *testing.T, s *Store, c JobTypeCommand) JobTypeState {
	t.Helper()
	value, err := s.CommitJobType(t.Context(), c, c.Value.Record.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestJobTypeCASImmutableVersionsAndTombstones(t *testing.T) {
	s := openCatalogMemory(t)
	create := jobTypeFixture(t, s, "probe")
	first := commitJobType(t, s, create)
	for _, id := range []string{"probe", "quote\"id", "<escaped>", "unicode-🙂"} {
		copy := first.Clone()
		copy.Current.Record.Key.ID = id
		raw, _ := json.Marshal(copy)
		key, _ := json.Marshal(id)
		cost, err := jobTypeStateCost(copy)
		if err != nil || cost != int64(len(raw)+len(key)+2) {
			t.Fatal("canonical quota accounting", id, cost, err)
		}
	}
	retried := commitJobType(t, s, create)
	if !reflect.DeepEqual(first, retried) {
		t.Fatal("exact retry changed original committed state")
	}
	if len(s.fsm.image.Catalog) != 0 || s.fsm.image.CatalogMutationSequence != 0 {
		t.Fatal("JobType leaked into ordinary catalog")
	}
	same := jobTypeChange(t, s, first, "v1")
	if _, err := s.CommitJobType(t.Context(), same, same.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeVersionConflict) {
		t.Fatal("unproved spec rewrite", err)
	}
	same.RetainedVersionRevision = first.Versions["v1"].Record.Revision
	metadata := commitJobType(t, s, same)
	if !reflect.DeepEqual(metadata.Versions["v1"], first.Versions["v1"]) || metadata.Current.Record.Revision == first.Current.Record.Revision {
		t.Fatal("metadata update replaced immutable version")
	}
	stale := jobTypeChange(t, s, first, "v2")
	if _, err := s.CommitJobType(t.Context(), stale, stale.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeConflict) {
		t.Fatal("stale CAS", err)
	}
	next := commitJobType(t, s, jobTypeChange(t, s, metadata, "v2"))
	if len(next.Versions) != 2 || !reflect.DeepEqual(next.Versions["v1"], first.Versions["v1"]) {
		t.Fatal("lost original version")
	}
	historical := jobTypeChange(t, s, next, "v1")
	historical.RetainedVersionRevision = first.Versions["v1"].Record.Revision
	if _, err := s.CommitJobType(t.Context(), historical, historical.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeVersionConflict) {
		t.Fatal("historical version reactivation accepted", err)
	}
	deletion := jobTypeChange(t, s, next, "v2")
	deletion.Action = "delete"
	deletion.Value.Record.Removed = true
	deletion.Value.Record.Payload = secureconfig.Envelope{}
	deleted := commitJobType(t, s, deletion)
	if len(deleted.Versions) != 2 || !deleted.Current.Record.Removed {
		t.Fatal("deletion discarded immutable versions")
	}
	page, _, err := s.JobTypes(t.Context(), "", 10)
	if err != nil || len(page) != 0 {
		t.Fatal("tombstone listed as active", err)
	}
	ids, err := s.JobTypeIDs(t.Context())
	if err != nil || !reflect.DeepEqual(ids, []string{"probe"}) {
		t.Fatal("startup cannot find tombstone", ids, err)
	}
	recreate := jobTypeFixture(t, s, "probe")
	recreate.Value.Record.CreatedAt = deleted.Current.Record.UpdatedAt.Add(time.Second)
	recreate.Value.Record.UpdatedAt = recreate.Value.Record.CreatedAt
	if _, err := s.CommitJobType(t.Context(), recreate, recreate.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeVersionConflict) {
		t.Fatal("recreation reused old version", err)
	}
	recreate.Value.Version = "v3"
	recreate.Value.Category = "recovery"
	if _, err := s.CommitJobType(t.Context(), recreate, recreate.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeVersionConflict) {
		t.Fatal("recreation changed category", err)
	}
	recreate.Value.Category = "check"
	final := commitJobType(t, s, recreate)
	if len(final.Versions) != 3 || final.Current.Record.UID == first.Current.Record.UID {
		t.Fatal("recreation identity or retention lost")
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestJobTypeAuthorityAndMalformedCommandsDoNotMutate(t *testing.T) {
	s := openCatalogMemory(t)
	c := jobTypeFixture(t, s, "probe")
	for name, change := range map[string]func(*JobTypeCommand){
		"actor":        func(c *JobTypeCommand) { c.Authority.Actor = "someone-else" },
		"epoch":        func(c *JobTypeCommand) { c.Authority.Epoch = "old-epoch" },
		"revision":     func(c *JobTypeCommand) { c.Authority.Revision = "old-revision" },
		"protocol":     func(c *JobTypeCommand) { c.Value.ProtocolVersion = "future" },
		"schema":       func(c *JobTypeCommand) { c.Value.SchemaProfile = "future" },
		"category":     func(c *JobTypeCommand) { c.Value.Category = "unknown" },
		"precommitted": func(c *JobTypeCommand) { c.Value.Record.CommittedIndex = 99 },
		"references":   func(c *JobTypeCommand) { c.Value.Record.References = []CatalogKey{{Kind: "Credential", ID: "secret"}} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			if _, err := s.CommitJobType(t.Context(), bad, bad.Value.Record.UpdatedAt); err == nil {
				t.Fatal("invalid command committed")
			}
			if s.fsm.image.JobTypes != nil {
				t.Fatal("invalid command changed state")
			}
		})
	}
	for _, gate := range []string{"revoked", "expired", "reset", "bootstrap", "restore"} {
		t.Run(gate, func(t *testing.T) {
			s := openCatalogMemory(t)
			c := jobTypeFixture(t, s, "probe")
			s.fsm.mu.Lock()
			switch gate {
			case "revoked":
				s.fsm.image.Authentication.Principals[0].Revoked = true
			case "expired":
				s.fsm.image.Authentication.Principals[0].ExpiresAt = c.Value.Record.UpdatedAt
			case "reset":
				s.fsm.image.Authentication.ResetRequired = true
			case "bootstrap":
				s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"}
			case "restore":
				s.fsm.image.Restore = &RestoreState{Phase: "actions"}
			}
			s.fsm.mu.Unlock()
			if _, err := s.CommitJobType(t.Context(), c, c.Value.Record.UpdatedAt); err == nil {
				t.Fatal("fenced mutation accepted")
			}
			if s.fsm.image.JobTypes != nil {
				t.Fatal("fenced mutation changed state")
			}
		})
	}
}

func TestJobTypeStrictWireFormatsAndBudget(t *testing.T) {
	s := openCatalogMemory(t)
	c := jobTypeFixture(t, s, "probe")
	command := Command{commandExtensions: commandExtensions{JobType: &c}, Kind: "job_type", At: c.Value.Record.UpdatedAt}
	for version := 1; version <= LatestFormatVersion+1; version++ {
		raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
		_, err := decodeEnvelope(raw)
		if (err == nil) != (externalStorageFormat(version)) {
			t.Fatal("format gate", version, err)
		}
	}
	raw, _ := json.Marshal(envelope{Version: JobTypeFormatVersion, Commands: []Command{command}})
	unknown := bytes.Replace(raw, []byte(`"action":"create"`), []byte(`"action":"create","future":null`), 1)
	if _, err := decodeEnvelope(unknown); err == nil {
		t.Fatal("unknown nested field accepted")
	}
	for _, kind := range []string{"recover", "barrier", "pulse", "authentication", "catalog"} {
		mixed := command
		mixed.Kind = kind
		if validateCommand(mixed) == nil {
			t.Fatal("extension attached to unrelated command", kind)
		}
	}
	if _, _, err := operationDigest(command); err == nil {
		t.Fatal("external command used old digest")
	}
	bound, err := encodedBound(command)
	encoded, _ := json.Marshal(command)
	if err != nil || bound < len(encoded) {
		t.Fatal("private embedded payload escaped size bound", bound, len(encoded), err)
	}
	c.Value.Record.Payload.Ciphertext = bytes.Repeat([]byte{'x'}, maxCommitBytes)
	if _, err := encodedBound(command); err == nil {
		t.Fatal("oversized extension escaped pre-encoding bound")
	}
	if _, err := s.Submit(t.Context(), []Command{command}); err == nil {
		t.Fatal("oversized command admitted")
	}
}

func TestJobTypeDetachedReadsAndSnapshotIsolation(t *testing.T) {
	s := openCatalogMemory(t)
	first := commitJobType(t, s, jobTypeFixture(t, s, "probe"))
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	commitJobType(t, s, jobTypeChange(t, s, first, "v2"))
	first.Current.Record.Payload.Ciphertext[0] ^= 1
	delete(first.Versions, "v1")
	current, ok, err := s.JobType(t.Context(), "probe")
	if err != nil || !ok || len(current.Versions) != 2 {
		t.Fatal("detached commit result mutated store", err)
	}
	current.Versions["v1"].Record.Payload.Ciphertext[0] ^= 1
	v, ok, err := s.JobTypeVersion(t.Context(), "probe", "v1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := catalogSealer(t).Open(t.Context(), v.Record.Binding(s.nodeID), v.Record.Payload); err != nil {
		t.Fatal("read mutated original encrypted version", err)
	}
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(jobTypeSnapshotMagic)) || bytes.Contains(sink.Bytes(), []byte("job-type-private")) {
		t.Fatal("snapshot framing or secrecy")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if len(i.JobTypes.Records["probe"].Versions) != 1 {
		t.Fatal("frozen snapshot saw later version")
	}
	for _, change := range []func(*image){
		func(i *image) { i.Version = CollectionReselectionFormatVersion },
		func(i *image) { i.JobTypes.EncodedBytes++ },
		func(i *image) {
			state := i.JobTypes.Records["probe"]
			state.Current.Category = "recovery"
			i.JobTypes.Records["probe"] = state
		},
		func(i *image) {
			state := i.JobTypes.Records["probe"]
			delete(state.Versions, "v1")
			i.JobTypes.Records["probe"] = state
		},
	} {
		bad := i
		bad.imageExtensions = cloneImageExtensions(i)
		change(&bad)
		if validateImageExtensions(bad) == nil {
			t.Fatal("corrupt snapshot accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.JobType(ctx, "probe"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read", err)
	}
	s.fsm.mu.Lock()
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, _, err = s.JobType(ctx, "probe")
	cancel()
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("held lock ignored context", err)
	}
}

func TestJobTypeRetainedVersionQuotaIsNotEviction(t *testing.T) {
	s := openCatalogMemory(t)
	state := commitJobType(t, s, jobTypeFixture(t, s, "probe"))
	for n := 2; n <= MaxJobTypeVersions; n++ {
		state = commitJobType(t, s, jobTypeChange(t, s, state, fmt.Sprintf("v%d", n)))
	}
	c := jobTypeChange(t, s, state, "too-many")
	if _, err := s.CommitJobType(t.Context(), c, c.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeQuota) {
		t.Fatal("version quota", err)
	}
	actual, _, err := s.JobType(t.Context(), "probe")
	if err != nil || !reflect.DeepEqual(state, actual) {
		t.Fatal("quota evicted or mutated retained state", err)
	}
	metadata := jobTypeChange(t, s, state, state.Current.Version)
	metadata.RetainedVersionRevision = state.Versions[state.Current.Version].Record.Revision
	if next := commitJobType(t, s, metadata); len(next.Versions) != MaxJobTypeVersions {
		t.Fatal("metadata update changed retained count")
	}
}

func TestJobTypeNativeSnapshotAndLogReplay(t *testing.T) {
	config := testConfig(t)
	s, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	state := commitJobType(t, s, jobTypeFixture(t, s, "probe"))
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	state = commitJobType(t, s, jobTypeChange(t, s, state, "v2"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := LockOffline(config.Storage.Directory)
	if err != nil {
		t.Fatal("stopped validation", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(config.Storage.Directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte("job-type-private")) {
			return errors.New("plaintext schema in storage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok, err := s.JobType(t.Context(), "probe")
	if err != nil || !ok || !reflect.DeepEqual(actual, state) {
		t.Fatal("native snapshot+log did not preserve exact versions", err)
	}
	if s.fsm.image.Version != JobTypeFormatVersion || len(s.fsm.image.Catalog) != 0 {
		t.Fatal("wrong restored storage namespace")
	}
	for _, version := range actual.Versions {
		plain, err := catalogSealer(t).Open(t.Context(), version.Record.Binding(s.nodeID), version.Record.Payload)
		if err != nil || !strings.HasPrefix(string(plain), "job-type-private") {
			t.Fatal("ciphertext binding lost", err)
		}
		clear(plain)
	}
	// The full streamed snapshot is necessary even with no collection rows.
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	sink := &collectionTestSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(sink.Bytes(), collectionPlanLedgerMagic)
	if cut < 0 {
		t.Fatal("format15 omitted format14 plan section")
	}
	if _, ledger, err := decodeSnapshot(io.LimitReader(bytes.NewReader(sink.Bytes()), int64(cut)), ""); err == nil {
		if ledger != nil {
			_ = ledger.Close()
		}
		t.Fatal("truncated format15 accepted")
	}
}

func TestJobTypeCiphertextQuotasAndBoundedPages(t *testing.T) {
	s := openCatalogMemory(t)
	large := jobTypeFixture(t, s, "large")
	plain := bytes.Repeat([]byte{'x'}, secureconfig.MaxPlaintext)
	defer clear(plain)
	seal := func(c *JobTypeCommand) {
		t.Helper()
		var err error
		c.Value.Record.Payload, err = catalogSealer(t).Seal(t.Context(), c.Value.Record.Binding(s.nodeID), plain)
		if err != nil {
			t.Fatal(err)
		}
	}
	seal(&large)
	state := commitJobType(t, s, large)
	for n := 2; ; n++ {
		if n > 10 {
			t.Fatal("one-ID byte quota did not bind")
		}
		command := jobTypeChange(t, s, state, fmt.Sprintf("v%d", n))
		seal(&command)
		next, err := s.CommitJobType(t.Context(), command, command.Value.Record.UpdatedAt)
		if errors.Is(err, ErrJobTypeQuota) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		state = next
	}
	actual, _, err := s.JobType(t.Context(), "large")
	if err != nil || !reflect.DeepEqual(actual, state) {
		t.Fatal("one-ID byte rejection changed state", err)
	}
	accepted := 1
	for n := 0; ; n++ {
		if n > 30 {
			t.Fatal("global byte quota did not bind")
		}
		command := jobTypeFixture(t, s, fmt.Sprintf("id-%03d", n))
		seal(&command)
		before := s.fsm.image.JobTypes.EncodedBytes
		_, err := s.CommitJobType(t.Context(), command, command.Value.Record.UpdatedAt)
		if errors.Is(err, ErrJobTypeQuota) {
			if s.fsm.image.JobTypes.EncodedBytes != before {
				t.Fatal("global byte rejection changed accounting")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	seen, after := 0, ""
	for {
		items, next, err := s.JobTypes(t.Context(), after, 100)
		if err != nil {
			t.Fatal(err)
		}
		size := 0
		for _, item := range items {
			raw, _ := json.Marshal(item)
			size += len(raw) + 4
			if item.Record.Key.ID <= after {
				t.Fatal("page order regressed")
			}
			after = item.Record.Key.ID
		}
		if size > MaxJobTypePageBytes || len(items) > 100 {
			t.Fatal("page exceeded bounds")
		}
		seen += len(items)
		if next == "" {
			break
		}
		if next != after {
			t.Fatal("wrong original continuation")
		}
	}
	if seen != accepted {
		t.Fatal("bounded pages lost current descriptors", seen, accepted)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestJobTypeCountQuotaPreservesRetainedIDs(t *testing.T) {
	s := openCatalogMemory(t)
	for offset := 0; offset < MaxJobTypes; {
		count := min(128, MaxJobTypes-offset)
		commands := make([]Command, 0, count)
		for n := 0; n < count; n++ {
			c := jobTypeFixture(t, s, fmt.Sprintf("id-%04d", offset+n))
			commands = append(commands, Command{commandExtensions: commandExtensions{JobType: &c}, Kind: "job_type", At: c.Value.Record.UpdatedAt})
		}
		results, err := s.Submit(t.Context(), commands)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Err != nil || !result.Allowed {
				t.Fatal("valid bounded batch failed", result.Err)
			}
		}
		offset += count
	}
	c := jobTypeFixture(t, s, "overflow")
	if _, err := s.CommitJobType(t.Context(), c, c.Value.Record.UpdatedAt); !errors.Is(err, ErrJobTypeQuota) {
		t.Fatal("ID count quota", err)
	}
	ids, err := s.JobTypeIDs(t.Context())
	if err != nil || len(ids) != MaxJobTypes {
		t.Fatal("quota evicted original IDs", err)
	}
	if err := validateImageExtensions(s.fsm.image); err != nil {
		t.Fatal(err)
	}
}

func TestJobTypeFormatPreservesExistingCollectionNamespaces(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 2)
	// The accepted execution fixture contains original input, plan, validation,
	// outcome and terminal namespaces. Adding JobTypes must retain every stream.
	before, err := s.fsm.collections.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	state := commitJobType(t, s, jobTypeFixture(t, s, "probe"))
	blob := captureSnapshotBytes(t, s.fsm)
	i, ledger, err := decodeSnapshot(bytes.NewReader(blob), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	after, err := ledger.Bytes()
	if err != nil || before != after || !reflect.DeepEqual(i.Collections[head.ID], head) || !reflect.DeepEqual(i.JobTypes.Records["probe"], state) {
		t.Fatal("mixed snapshot lost original state", before, after, err)
	}
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := validateCollectionRows(i, view); err != nil {
		t.Fatal("mixed namespace recovery certification", err)
	}
}
