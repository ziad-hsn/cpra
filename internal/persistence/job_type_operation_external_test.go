//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func reserveJobType(t *testing.T, s *Store, c JobTypeCommand) (JobTypeCommand, OperationReservation) {
	t.Helper()
	r, err := s.ReserveJobTypeOperation(t.Context(), c, c.Value.Record.UpdatedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatal("reserve JobType", err)
	}
	c.OperationID = r.ID
	return c, r
}
func commitJobTypeOperation(t *testing.T, s *Store, c JobTypeCommand, r OperationReservation) JobTypeCommitResult {
	t.Helper()
	got, err := s.CommitJobTypeOperation(t.Context(), c, r.At.Add(time.Millisecond))
	if err != nil {
		t.Fatal("commit JobType operation", err)
	}
	return got
}

func TestJobTypeOperationAtomicReceiptsAndSnapshot(t *testing.T) {
	s := openCatalogMemory(t)
	c, reservation := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
	if _, ok, err := s.JobType(t.Context(), "probe"); err != nil || ok {
		t.Fatal("reservation changed resource", err)
	}
	observed, err := s.OperationContext(t.Context(), reservation.ID, reservation.At)
	if err != nil || observed != reservation.OperationReceipt {
		t.Fatal("reserved observation", err)
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	got := commitJobTypeOperation(t, s, c, reservation)
	r := got.Operation
	if r.ID != reservation.ID || r.Actor != c.Authority.Actor || r.State != "completed" || r.Outcome != "applied" || r.CommittedIndex != got.State.Current.Record.CommittedIndex || r.At != reservation.At || r.Key != c.Value.Record.Key || r.NewVersion != c.Value.Record.Revision || !got.State.Current.Record.UpdatedAt.Equal(c.Value.Record.UpdatedAt) {
		t.Fatal("wrong exact commit receipt", r)
	}
	if len(s.fsm.image.OperationReservations) != 0 || len(s.fsm.image.Operations) != 0 || len(s.fsm.image.Catalog) != 0 {
		t.Fatal("terminal metadata commit retained executable work")
	}
	observed, err = s.OperationContext(t.Context(), r.ID, r.UpdatedAt)
	if err != nil || observed != r {
		t.Fatal("terminal history observation", err)
	}
	page, err := s.History().Page("resource/JobType/probe", "", 20)
	if err != nil || len(page.Events) != 1 || page.Events[0].Type != "configuration_applied" || page.Events[0].Operation == nil || *page.Events[0].Operation != r {
		t.Fatal("atomic nonsecret audit", page, err)
	}
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, []byte("job-type-private")) || bytes.Contains(raw, []byte("ciphertext")) || bytes.Contains(raw, []byte("wrapped_key")) || bytes.Contains(raw, []byte(authenticationToken)) {
		t.Fatal("audit exposed configuration or credential")
	}
	view, err := s.OperationSnapshot(r.UpdatedAt, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := view.Page(t.Context(), "", "", 100)
	if err != nil || len(rows) != 1 || rows[0] != r {
		t.Fatal("ordinary operation list omitted terminal JobType", rows, err)
	}
	// Exact frozen format16 allocator state restores before the resource commit.
	sink := &collectionTestSink{}
	if err := frozen.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(sink.Bytes(), []byte(jobTypeOperationSnapshotMagic)) {
		t.Fatal("wrong reserved snapshot format")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(sink.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if i.JobTypes != nil || i.OperationReservations[r.ID] != reservation {
		t.Fatal("snapshot was not detached from later commit")
	}
	i.Version = JobTypeFormatVersion
	if validateImageExtensions(i) == nil {
		t.Fatal("format15 accepted format16 reservations")
	}
	// Success can only be reconciled by the original handle, never executed twice.
	if _, err := s.CommitJobTypeOperation(t.Context(), c, r.UpdatedAt.Add(time.Second)); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("consumed operation repeated", err)
	}
	update := jobTypeChange(t, s, got.State, "v1")
	update.RetainedVersionRevision = got.State.Versions["v1"].Record.Revision
	update, ur := reserveJobType(t, s, update)
	next := commitJobTypeOperation(t, s, update, ur)
	if !reflect.DeepEqual(next.State.Versions["v1"], got.State.Versions["v1"]) || next.Operation.OldVersion != got.State.Current.Record.Revision {
		t.Fatal("metadata version/receipt changed immutable implementation")
	}
	deletion := jobTypeChange(t, s, next.State, "v1")
	deletion.Action = "delete"
	deletion.Value.Record.Removed = true
	deletion.Value.Record.Payload = secureconfig.Envelope{}
	deletion, dr := reserveJobType(t, s, deletion)
	deleted := commitJobTypeOperation(t, s, deletion, dr)
	if !deleted.Operation.Removed || !deleted.State.Current.Record.Removed || len(deleted.State.Versions) != 1 {
		t.Fatal("delete receipt or version retention")
	}
}

func TestJobTypeOperationImmutableIntentAndFailedCAS(t *testing.T) {
	s := openCatalogMemory(t)
	c, r := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
	for name, change := range map[string]func(*JobTypeCommand){
		"ciphertext": func(c *JobTypeCommand) { c.Value.Record.Payload.Ciphertext[0] ^= 1 },
		"revision":   func(c *JobTypeCommand) { c.Value.Record.Revision = uuid.NewString() },
		"version":    func(c *JobTypeCommand) { c.Value.Version = "different" },
		"handler":    func(c *JobTypeCommand) { c.Value.Handler = "different" },
		"prepared-time": func(c *JobTypeCommand) {
			c.Value.Record.UpdatedAt = c.Value.Record.UpdatedAt.Add(time.Microsecond)
			c.Value.Record.CreatedAt = c.Value.Record.UpdatedAt
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			bad.Value = c.Value.Clone()
			change(&bad)
			if _, err := s.CommitJobTypeOperation(t.Context(), bad, r.At.Add(time.Second)); !errors.Is(err, ErrOperationReservation) {
				t.Fatal("changed reserved body", err)
			}
			if s.fsm.image.JobTypes != nil || s.fsm.image.OperationReservations[r.ID] != r {
				t.Fatal("rejected body mutated state")
			}
		})
	}
	good := commitJobTypeOperation(t, s, c, r)
	conflicting := jobTypeFixture(t, s, "probe")
	conflicting, cr := reserveJobType(t, s, conflicting)
	rejected, err := s.CommitJobTypeOperation(t.Context(), conflicting, cr.At.Add(time.Millisecond))
	if !errors.Is(err, ErrJobTypeConflict) || rejected.Operation.ID != cr.ID || rejected.Operation.Outcome != "activation_rejected" || rejected.Operation.CommittedIndex != 0 {
		t.Fatal("CAS failure lost terminal original identity", rejected.Operation, err)
	}
	observed, err := s.OperationContext(t.Context(), cr.ID, cr.At.Add(time.Second))
	if err != nil || observed != rejected.Operation {
		t.Fatal("CAS history", err)
	}
	current, _, _ := s.JobType(t.Context(), "probe")
	if !reflect.DeepEqual(current, good.State) {
		t.Fatal("failed CAS changed resource")
	}
	if _, err := s.CommitJobTypeOperation(t.Context(), conflicting, cr.At.Add(time.Second)); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("failed CAS reservation reused", err)
	}
}

func TestJobTypeOperationAuthorityExpiryAndAllocationBypass(t *testing.T) {
	for _, phase := range []string{"reserve", "commit"} {
		for _, gate := range []string{"revoked", "expired", "revision", "reset", "bootstrap", "restore"} {
			t.Run(phase+"/"+gate, func(t *testing.T) {
				s := openCatalogMemory(t)
				c := jobTypeFixture(t, s, "probe")
				at := c.Value.Record.UpdatedAt.Add(time.Millisecond)
				var reserved OperationReservation
				if phase == "commit" {
					c, reserved = reserveJobType(t, s, c)
					at = reserved.At.Add(time.Millisecond)
				}
				s.fsm.mu.Lock()
				switch gate {
				case "revoked":
					s.fsm.image.Authentication.Principals[0].Revoked = true
				case "expired":
					s.fsm.image.Authentication.Principals[0].ExpiresAt = at
				case "revision":
					s.fsm.image.Authentication.Revision = uuid.NewString()
				case "reset":
					s.fsm.image.Authentication.ResetRequired = true
				case "bootstrap":
					s.fsm.image.Bootstrap = &BootstrapState{Phase: "seeding"}
				case "restore":
					s.fsm.image.Restore = &RestoreState{Phase: "actions"}
				}
				s.fsm.mu.Unlock()
				var err error
				if phase == "reserve" {
					_, err = s.ReserveJobTypeOperation(t.Context(), c, at)
				} else {
					_, err = s.CommitJobTypeOperation(t.Context(), c, at)
				}
				if err == nil || s.fsm.image.JobTypes != nil {
					t.Fatal("fenced mutation accepted", err)
				}
				if phase == "reserve" && s.fsm.image.OperationHighWater != 0 {
					t.Fatal("fenced allocation issued handle")
				}
				if phase == "commit" && s.fsm.image.OperationReservations[reserved.ID] != reserved {
					t.Fatal("authority failure consumed unrelated intent")
				}
			})
		}
	}
	s := openCatalogMemory(t)
	c := jobTypeFixture(t, s, "probe")
	// Long-lived authority isolates reservation expiry from credential expiry.
	s.fsm.mu.Lock()
	s.fsm.image.Authentication.Principals[0].ExpiresAt = time.Time{}
	s.fsm.mu.Unlock()
	c, r := reserveJobType(t, s, c)
	expired, err := s.CommitJobTypeOperation(t.Context(), c, r.ExpiresAt)
	if !errors.Is(err, ErrOperationExpired) || expired.Operation.Outcome != "reservation_expired" || s.fsm.image.JobTypes != nil {
		t.Fatal("expired reservation activated", err)
	}
	if _, err := s.OperationContext(t.Context(), r.ID, r.ExpiresAt); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("expired observation", err)
	}
	pending, _ := jobTypeOperationReservation(c, r.At)
	allocation := OperationAllocation{Epoch: uuid.NewString(), Reservation: pending}
	if validateCommand(Command{Kind: "operation_reserve", At: r.At, OperationAllocation: &allocation}) == nil {
		t.Fatal("generic allocator bypassed named authority")
	}
	bad := JobTypeOperationAllocation{Allocation: allocation, Authority: c.Authority}
	bad.Authority.Actor = "another"
	if bad.validate(r.At) == nil {
		t.Fatal("allocation actor not bound")
	}
}

func TestJobTypeOperationLostRepliesAndHistoryFailure(t *testing.T) {
	for _, phase := range []string{"allocation", "commit"} {
		t.Run(phase, func(t *testing.T) {
			seed := openCatalogMemory(t)
			c := jobTypeFixture(t, seed, "probe")
			dormant, start := dormantCatalogStore(t)
			auth := seed.fsm.image.Authentication.Clone()
			dormant.fsm.image.Authentication = &auth
			var reserved OperationReservation
			if phase == "commit" {
				r, err := jobTypeOperationReservation(c, c.Value.Record.UpdatedAt)
				if err != nil {
					t.Fatal(err)
				}
				result := dormant.fsm.applyJobTypeOperationAllocation(JobTypeOperationAllocation{Allocation: OperationAllocation{Epoch: uuid.NewString(), Reservation: r}, Authority: c.Authority}, r.At)
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				reserved = *result.Reservation
				c.OperationID = reserved.ID
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				if phase == "allocation" {
					r, err := dormant.ReserveJobTypeOperation(ctx, c, c.Value.Record.UpdatedAt)
					if r.ID != "" {
						done <- errors.New("uncertain allocation exposed handle")
						return
					}
					done <- err
				} else {
					_, err := dormant.CommitJobTypeOperation(ctx, c, reserved.At.Add(time.Millisecond))
					done <- err
				}
			}()
			waitBudgetCondition(t, "queued JobType "+phase, func() bool { return len(dormant.requests) == 1 })
			cancel()
			err := <-done
			if phase == "allocation" {
				if !errors.Is(err, ErrOperationAllocationUnconfirmed) || errors.Is(err, ErrCommitUnconfirmed) {
					t.Fatal("allocation ambiguity", err)
				}
			} else if !errors.Is(err, ErrCommitUnconfirmed) {
				t.Fatal("commit ambiguity", err)
			}
			start()
			if err := dormant.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if phase == "allocation" {
				if dormant.fsm.image.JobTypes != nil || len(dormant.fsm.image.OperationReservations) != 1 {
					t.Fatal("uncertain allocation applied resource")
				}
			} else {
				r, err := dormant.OperationContext(t.Context(), reserved.ID, reserved.At.Add(time.Second))
				if err != nil || r.State != "completed" || r.ID != reserved.ID {
					t.Fatal("lost reply original handle reconciliation", err)
				}
				if dormant.fsm.image.OperationHighWater != 1 || len(dormant.fsm.image.OperationReservations) != 0 {
					t.Fatal("lost response invented another mutation")
				}
			}
		})
	}
	t.Run("history commit fails closed", func(t *testing.T) {
		s := openCatalogMemory(t)
		c, r := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
		fault := errors.New("injected durable history failure")
		s.fsm.history.mu.Lock()
		s.fsm.history.err = fault
		s.fsm.history.mu.Unlock()
		_, err := s.CommitJobTypeOperation(t.Context(), c, r.At.Add(time.Millisecond))
		if !errors.Is(err, ErrCommitUnconfirmed) || !errors.Is(err, fault) || s.Status().Ready {
			t.Fatal("history failure reported confirmed commit", err)
		}
		if _, err := s.OperationContext(t.Context(), r.ID, r.At); err == nil {
			t.Fatal("failed history exposed healthy receipt")
		}
	})
}

func TestJobTypeOperationNativeRestartRestoreAndRetention(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot-%t", snapshot), func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			c, r := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
			committed := commitJobTypeOperation(t, s, c, r)
			pending, pr := reserveJobType(t, s, jobTypeFixture(t, s, "pending"))
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := s.OperationContext(t.Context(), r.ID, r.At.Add(time.Second))
			if err != nil || observed != committed.Operation {
				t.Fatal("restart lost terminal audit", err)
			}
			observed, err = s.OperationContext(t.Context(), pr.ID, pr.At)
			if err != nil || observed != pr.OperationReceipt {
				t.Fatal("restart lost original reservation", err)
			}
			state, ok, err := s.JobType(t.Context(), "probe")
			if err != nil || !ok || !reflect.DeepEqual(state, committed.State) {
				t.Fatal("restart changed version state", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := MarkRestored(config.Storage.Directory, time.Now().UTC().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			admin := openAuthenticationAdmin(t, config)
			auth, err := admin.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			_, err = admin.CommitAuthentication(t.Context(), AuthenticationCommand{Mode: "provision", Epoch: auth.Epoch, ExpectedEpoch: auth.Epoch, ExpectedRevision: auth.Revision, Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC().Add(2 * time.Second), Principals: authenticationBootstrap().Principals})
			if err != nil {
				t.Fatal(err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{r.ID, pr.ID} {
				if _, err := s.OperationContext(t.Context(), id, time.Now().UTC().Add(3*time.Second)); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("restore retained old operation epoch", err)
				}
			}
			if _, err := s.CommitJobTypeOperation(t.Context(), pending, time.Now().UTC().Add(3*time.Second)); err == nil {
				t.Fatal("restore accepted stale authority/operation")
			}
			page, err := s.History().Page("resource/JobType/pending", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range page.Events {
				if e.Operation != nil && e.Operation.ID == pr.ID && e.Reason == "explicit_restore" {
					found = true
				}
			}
			if !found {
				t.Fatal("restore omitted original reservation audit")
			}
			state, ok, err = s.JobType(t.Context(), "probe")
			if err != nil || !ok || !reflect.DeepEqual(state, committed.State) {
				t.Fatal("restore altered immutable config", err)
			}
		})
	}
	s := openCatalogMemory(t)
	c, r := reserveJobType(t, s, jobTypeFixture(t, s, "expire-history"))
	commitJobTypeOperation(t, s, c, r)
	if err := s.History().Expire(r.At.Add(32 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OperationContext(t.Context(), r.ID, r.At.Add(32*24*time.Hour)); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("retained highwater treated expired handle as unknown", err)
	}
}

func TestJobTypeOperationFormatAndLegacyDigest(t *testing.T) {
	s := openCatalogMemory(t)
	c, r := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
	commands := []Command{{Kind: "job_type", At: r.At, commandExtensions: commandExtensions{JobType: &c}}}
	allocation, _ := jobTypeOperationReservation(c, r.At)
	a := JobTypeOperationAllocation{Allocation: OperationAllocation{Epoch: uuid.NewString(), Reservation: allocation}, Authority: c.Authority}
	commands = append(commands, Command{Kind: "job_type_reserve", At: r.At, commandExtensions: commandExtensions{JobTypeAllocation: &a}})
	for _, command := range commands {
		for version := 1; version <= LatestFormatVersion+1; version++ {
			raw, _ := json.Marshal(envelope{Version: version, Commands: []Command{command}})
			_, err := decodeEnvelope(raw)
			if (err == nil) != (version == JobTypeOperationFormatVersion || version == WorkerPolicyFormatVersion || version == CatalogJobTypeFormatVersion || version == WorkerSessionFormatVersion || version == WorkerExecutionFormatVersion || version == WorkerOfferFormatVersion) {
				t.Fatal("operation format bypass", command.Kind, version, err)
			}
		}
		raw, _ := json.Marshal(command)
		bound, err := encodedBound(command)
		if err != nil || bound < len(raw) {
			t.Fatal("extension budget", err)
		}
		if _, _, err := operationDigest(command); err == nil {
			t.Fatal("extension entered historical digest")
		}
	}
	c.OperationID = ""
	raw, _ := json.Marshal(c)
	old, _ := json.Marshal(jobTypeCommandV1(c))
	if !bytes.Equal(raw, old) {
		t.Fatal("omitted operation field changed format15 encoding")
	}
	current, frozen := reflect.TypeFor[JobTypeCommand](), reflect.TypeFor[jobTypeCommandDigestV1]()
	if current.NumField() != frozen.NumField()+1 {
		t.Fatal("unreviewed command projection")
	}
	for n := 0; n < frozen.NumField(); n++ {
		field := frozen.Field(n)
		other, ok := current.FieldByName(field.Name)
		if !ok || field.Type != other.Type || field.Tag != other.Tag {
			t.Fatal("historical shape changed", field.Name)
		}
	}
	// This independently pins the old serialized body, including empty encrypted
	// envelope fields and every old optional guard, rather than hashing itself.
	at := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	fixed := JobTypeCommand{Action: "replace", Value: JobTypeVersion{Record: CatalogRecord{Key: CatalogKey{Kind: "JobType", ID: "id"}, UID: "uid", Revision: "revision", Generation: 2, Purpose: "job-type", CreatedAt: at, UpdatedAt: at}, Version: "1", Category: "check", Handler: "handler", ProtocolVersion: "1", SchemaProfile: "cpra.schema.v1"}, ExpectedUID: "uid", ExpectedRevision: "previous", RetainedVersionRevision: "original", Authority: OperatorAuthority{Actor: "operator", Epoch: "epoch", Revision: "policy"}}
	const want = "5e5bdf9cb55c865644781e85ef26ab113b7b90e7111d3c0445af90b005772c27"
	if got := jobTypeCommandDigest(fixed, at); got != want {
		t.Fatalf("format15 golden %s", got)
	}
}

func TestJobTypeSnapshotDetachedChargeAndContext(t *testing.T) {
	s := openCatalogMemory(t)
	b := commitJobType(t, s, jobTypeFixture(t, s, "b"))
	commitJobType(t, s, jobTypeFixture(t, s, "a"))
	rows, charge, err := s.JobTypeSnapshot(t.Context())
	if err != nil || len(rows) != 2 || rows[0].Record.Key.ID != "a" || rows[1].Record.Key.ID != "b" {
		t.Fatal("sorted snapshot", err)
	}
	var exact int64
	for _, v := range rows {
		raw, _ := json.Marshal(v)
		exact += int64(len(raw) + 4)
	}
	if charge != exact {
		t.Fatal("incorrect canonical charge", charge, exact)
	}
	commitJobType(t, s, jobTypeChange(t, s, b, "v2"))
	if rows[1].Version != "v1" {
		t.Fatal("snapshot saw future version")
	}
	rows[0].Record.Payload.Ciphertext[0] ^= 1
	v, _, _ := s.JobTypeVersion(t.Context(), "a", "v1")
	if _, err := catalogSealer(t).Open(t.Context(), v.Record.Binding(s.nodeID), v.Record.Payload); err != nil {
		t.Fatal("snapshot leaked mutable slices", err)
	}
	s.fsm.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, _, err = s.JobTypeSnapshot(ctx)
	cancel()
	s.fsm.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("snapshot lock ignored cancellation", err)
	}
}

func TestJobTypeOperationNativeCommittedReplyLoss(t *testing.T) {
	config := testConfig(t)
	s, err := Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, r := reserveJobType(t, s, jobTypeFixture(t, s, "lost-native-reply"))
	previous := s.raft.CommitIndex()
	// The real Raft log can commit while the FSM owner deliberately waits. Cancel
	// only after the replicated index advances, then release the owner and read
	// the original receipt rather than resubmitting the mutation.
	s.fsm.transition.Lock()
	locked := true
	defer func() {
		if locked {
			s.fsm.transition.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.CommitJobTypeOperation(ctx, c, r.At.Add(time.Millisecond)); done <- err }()
	waitBudgetCondition(t, "native JobType log committed", func() bool { return s.raft.CommitIndex() > previous })
	cancel()
	if err := <-done; !errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("lost native reply was not uncertain", err)
	}
	s.fsm.transition.Unlock()
	locked = false
	if err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.OperationContext(t.Context(), r.ID, r.At.Add(time.Second))
	if err != nil || receipt.State != "completed" || receipt.Outcome != "applied" {
		t.Fatal("original commit could not be reconciled", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.OperationContext(t.Context(), r.ID, r.At.Add(time.Second))
	if err != nil || after != receipt || s.fsm.image.OperationHighWater != 1 {
		t.Fatal("restart changed lost-reply identity", err)
	}
	state, ok, err := s.JobType(t.Context(), "lost-native-reply")
	if err != nil || !ok || state.Current.Record.CommittedIndex != receipt.CommittedIndex {
		t.Fatal("receipt and resource did not recover together", err)
	}
}

func TestJobTypeOperationSnapshotPreservesCollectionSections(t *testing.T) {
	s, head := executionRetirementFixture(t, false, 2)
	before, err := s.fsm.collections.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	c, r := reserveJobType(t, s, jobTypeFixture(t, s, "probe"))
	committed := commitJobTypeOperation(t, s, c, r)
	blob := captureSnapshotBytes(t, s.fsm)
	if !bytes.HasPrefix(blob, []byte(jobTypeOperationSnapshotMagic)) {
		t.Fatal("format16 snapshot framing missing")
	}
	i, ledger, err := decodeSnapshot(bytes.NewReader(blob), "")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	after, err := ledger.Bytes()
	if err != nil || before != after || !reflect.DeepEqual(i.Collections[head.ID], head) || !reflect.DeepEqual(i.JobTypes.Records["probe"], committed.State) {
		t.Fatal("format16 lost an older source or execution namespace", err)
	}
	view, err := ledger.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := validateCollectionRows(i, view); err != nil {
		t.Fatal("format16 recovery failed original proof", err)
	}
}

func TestJobTypeOperationCanceledBeforeAdmissionAndExactMetadata(t *testing.T) {
	s := openCatalogMemory(t)
	c := jobTypeFixture(t, s, "probe")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ReserveJobTypeOperation(ctx, c, c.Value.Record.UpdatedAt); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled allocation", err)
	}
	if s.fsm.image.OperationHighWater != 0 {
		t.Fatal("canceled allocation issued handle")
	}
	c, r := reserveJobType(t, s, c)
	if _, err := s.CommitJobTypeOperation(ctx, c, r.At); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled commit", err)
	}
	if s.fsm.image.JobTypes != nil || s.fsm.image.OperationReservations[r.ID] != r {
		t.Fatal("pre-canceled commit changed state")
	}
	for _, change := range []func(*OperationReservation){
		func(r *OperationReservation) { r.Actor = "other" }, func(r *OperationReservation) { r.Generation++ },
		func(r *OperationReservation) { r.Key.ID = "other" }, func(r *OperationReservation) { r.NewVersion = "other" },
		func(r *OperationReservation) { r.CommandKind = "catalog" }, func(r *OperationReservation) { r.Removed = true },
	} {
		damaged := r
		change(&damaged)
		s.fsm.mu.Lock()
		s.fsm.image.OperationReservations[r.ID] = damaged
		s.fsm.mu.Unlock()
		if _, err := s.CommitJobTypeOperation(t.Context(), c, r.At.Add(time.Millisecond)); !errors.Is(err, ErrOperationReservation) {
			t.Fatal("digest alone authorized mismatched reservation metadata", err)
		}
	}
	s.fsm.mu.Lock()
	s.fsm.image.OperationReservations[r.ID] = r
	s.fsm.mu.Unlock()
	commitJobTypeOperation(t, s, c, r)
}
