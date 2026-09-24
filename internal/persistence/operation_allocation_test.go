package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func reservationCommand(t *testing.T, s *Store, id string, at time.Time) Command {
	t.Helper()
	r := catalogRecord(t, s, "Monitor", id, uuid.NewString(), uuid.NewString(), "operation-private-provider-secret")
	r.CreatedAt, r.UpdatedAt = at, at
	m := operationMutation(CatalogMutation{Record: r, Create: true})
	return Command{Kind: "catalog", At: at, Catalog: &m}
}
func allocatedCommand(t *testing.T, s *Store, c Command) (OperationReservation, Command) {
	t.Helper()
	r, err := s.ReserveOperation(context.Background(), c)
	if err != nil {
		t.Fatal("reserve", err)
	}
	switch c.Kind {
	case "catalog":
		b := *c.Catalog
		b.OperationID = r.ID
		c.Catalog = &b
	case "control":
		b := *c.Control
		b.OperationID = r.ID
		c.Control = &b
	case "manual_recovery":
		b := *c.ManualRecovery
		b.OperationID = r.ID
		c.ManualRecovery = &b
	case "action_review":
		b := *c.ActionReview
		b.OperationID = r.ID
		c.ActionReview = &b
	}
	c.At = c.At.Add(time.Millisecond)
	return r, c
}
func TestOperationHandleCanonicalBoundaries(t *testing.T) {
	epoch := "12345678-9abc-4def-8abc-123456789abc"
	for _, seq := range []uint64{1, 9, math.MaxUint64} {
		id := operationHandle(epoch, seq)
		e, s, err := ParseOperationHandle(id)
		if err != nil || e != epoch || s != seq || len(id) != 60 {
			t.Fatal(id, e, s, err)
		}
	}
	for _, id := range []string{"", uuid.NewString(), operationHandle(epoch, 0), operationHandle(uuid.Nil.String(), 1), operationHandle(strings.ToUpper(epoch), 1), "op." + epoch + ".18446744073709551616", "op." + epoch + ".0000000000000000001", "op." + epoch + ".+0000000000000000001", "op." + epoch + ".00000000000000000001\n"} {
		if _, _, err := ParseOperationHandle(id); err == nil {
			t.Fatalf("noncanonical handle accepted %q", id)
		}
	}
}
func TestOperationAllocationBindsContentAndRetiresRejection(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	base := reservationCommand(t, s, "reserved", at)
	r, c := allocatedCommand(t, s, base)
	if r.ID == base.Catalog.Record.Revision || r.CommittedIndex != 0 || r.State != "reserved" {
		t.Fatal("allocation claimed active change", r)
	}
	if _, ok, _ := s.CatalogGet(base.Catalog.Record.Key); ok {
		t.Fatal("reservation applied catalog")
	}
	if got, err := s.Operation(r.ID); err != nil || got.State != "reserved" || got.CommittedIndex != 0 {
		t.Fatal(got, err)
	}
	for name, change := range map[string]func(*CatalogMutation){
		"actor":    func(m *CatalogMutation) { m.Actor = "another-actor" },
		"uid":      func(m *CatalogMutation) { m.Record.UID = uuid.NewString() },
		"revision": func(m *CatalogMutation) { m.Record.Revision = uuid.NewString() },
		"payload":  func(m *CatalogMutation) { m.Record.Payload.Ciphertext[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			body := c.Catalog.Clone()
			change(&body)
			bad.Catalog = &body
			if got := submit(t, s, bad)[0]; !errors.Is(got.Err, ErrOperationReservation) {
				t.Fatal("changed reservation accepted", got.Err)
			}
		})
	}
	backwards := c
	backwards.At = at.Add(-time.Nanosecond)
	if got := submit(t, s, backwards)[0]; !errors.Is(got.Err, ErrOperationReservation) {
		t.Fatal("clock regression accepted", got.Err)
	}
	applied := submit(t, s, c)[0]
	if applied.Err != nil || !applied.Allowed || applied.Operation.ID != r.ID || applied.Operation.NewVersion != base.Catalog.Record.Revision || applied.Operation.CommittedIndex == 0 {
		t.Fatal(applied)
	}
	if got := submit(t, s, c)[0]; !errors.Is(got.Err, ErrOperationExpired) {
		t.Fatal("consumed operation could be replayed", got.Err)
	}
	if got, ok := s.PendingOperationForVersion(applied.Catalog.Key, applied.Catalog.UID, "", applied.Catalog.Revision); !ok || got.ID != r.ID {
		t.Fatal("separate version index failed", got, ok)
	}
	completeReceipt(t, s, *applied.Operation, true)
	if got, ok := s.PendingOperationForVersion(applied.Catalog.Key, applied.Catalog.UID, "", applied.Catalog.Revision); ok {
		t.Fatal("completed index retained", got)
	}
	other := reservationCommand(t, s, "reserved", c.At.Add(time.Second))
	rejected, target := allocatedCommand(t, s, other)
	result := submit(t, s, target)[0]
	if !errors.Is(result.Err, ErrCatalogConflict) || result.Operation == nil || result.Operation.Outcome != "activation_rejected" || result.Operation.CommittedIndex != 0 {
		t.Fatal("rejected activation receipt lied", result)
	}
	if got, err := s.Operation(rejected.ID); err != nil || got.State != "failed" || got.CommittedIndex != 0 {
		t.Fatal(got, err)
	}
	if got := submit(t, s, target)[0]; !errors.Is(got.Err, ErrOperationExpired) {
		t.Fatal("rejected activation resurrected", got.Err)
	}
	snapshot, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Release()
	raw, _ := json.Marshal(snapshot.(*frozenSnapshot).image)
	if bytes.Contains(raw, []byte("operation-private-provider-secret")) {
		t.Fatal("reservation persisted provider secret")
	}
	if _, err = decodeImage(bytes.NewReader(raw)); err != nil {
		t.Fatal("allocator snapshot failed", err)
	}
}
func TestOperationAllocationUnconfirmedDoesNotSubmitTarget(t *testing.T) {
	s, start := dormantCatalogStore(t)
	c := reservationCommand(t, s, "no-active-change", time.Now().UTC())
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		r, err := s.ReserveOperation(ctx, c)
		if r.ID != "" {
			result <- errors.New("unconfirmed allocation returned handle")
			return
		}
		result <- err
	}()
	waitBudgetCondition(t, "accepted reservation", func() bool { return len(s.requests) == 1 })
	cancel()
	if err := <-result; !errors.Is(err, ErrOperationAllocationUnconfirmed) || errors.Is(err, ErrCommitUnconfirmed) {
		t.Fatal("allocation uncertainty misclassified", err)
	}
	start()
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CatalogGet(c.Catalog.Record.Key); ok {
		t.Fatal("uncertain allocation changed resource")
	}
	s.fsm.mu.RLock()
	count, high := len(s.fsm.image.OperationReservations), s.fsm.image.OperationHighWater
	s.fsm.mu.RUnlock()
	if count != 1 || high != 1 {
		t.Fatal("committed lost-reply reservation missing", count, high)
	}
	next, err := s.ReserveOperation(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, sequence, _ := ParseOperationHandle(next.ID)
	if sequence != 2 {
		t.Fatal("unconfirmed handle reused", next.ID)
	}
}
func TestOperationReservationExpiryAndIssuedRetirement(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	r, c := allocatedCommand(t, s, reservationCommand(t, s, "expired", at))
	c.At = r.ExpiresAt
	got := submit(t, s, c)[0]
	if !errors.Is(got.Err, ErrOperationExpired) || got.Operation.CommittedIndex != 0 {
		t.Fatal("expired activation succeeded", got)
	}
	if _, ok, _ := s.CatalogGet(c.Catalog.Record.Key); ok {
		t.Fatal("expired mutation applied")
	}
	if _, err := s.Operation(r.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("expired reservation still usable", err)
	}
	epoch, _, _ := ParseOperationHandle(r.ID)
	if _, err := s.Operation(operationHandle(epoch, 2)); !errors.Is(err, ErrOperationNotFound) {
		t.Fatal("unissued handle not 404", err)
	}
	if _, err := s.Operation(operationHandle(uuid.NewString(), 1)); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("foreign epoch not expired", err)
	}
	live, target := allocatedCommand(t, s, reservationCommand(t, s, "live", at))
	applied := submit(t, s, target)[0]
	if applied.Err != nil {
		t.Fatal(applied.Err)
	}
	completeReceipt(t, s, *applied.Operation, true)
	if err := s.History().Expire(at.Add(32 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Operation(live.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("issued history loss became unknown", err)
	}
	if got := submit(t, s, target)[0]; !errors.Is(got.Err, ErrOperationExpired) {
		t.Fatal("expired history permitted replay", got.Err)
	}
}
func TestOperationAllocationConcurrentAndSnapshotValidation(t *testing.T) {
	s := openCatalogMemory(t)
	base := reservationCommand(t, s, "concurrent", time.Now().UTC())
	var wg sync.WaitGroup
	results := make(chan OperationReservation, 32)
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ReserveOperation(context.Background(), base)
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[uint64]bool{}
	var epoch string
	for r := range results {
		e, seq, err := ParseOperationHandle(r.ID)
		if err != nil || seen[seq] || epoch != "" && e != epoch {
			t.Fatal("allocation race", r.ID, err)
		}
		epoch = e
		seen[seq] = true
	}
	if len(seen) != 32 || !seen[1] || !seen[32] {
		t.Fatal(seen)
	}
	frozen, err := s.fsm.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	raw, _ := json.Marshal(frozen.(*frozenSnapshot).image)
	for name, mutate := range map[string]func(*image){
		"highwater": func(i *image) { i.OperationHighWater = 1 },
		"epoch":     func(i *image) { i.OperationEpoch = uuid.NewString() },
		"nil epoch": func(i *image) { i.OperationEpoch = "" },
		"missing digest": func(i *image) {
			for id, r := range i.OperationReservations {
				r.Digest = ""
				i.OperationReservations[id] = r
				break
			}
		},
		"target commits": func(i *image) {
			for id, r := range i.OperationReservations {
				r.CommittedIndex = 1
				i.OperationReservations[id] = r
				break
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var i image
			if err := json.Unmarshal(raw, &i); err != nil {
				t.Fatal(err)
			}
			mutate(&i)
			b, _ := json.Marshal(i)
			if _, err := decodeImage(bytes.NewReader(b)); err == nil {
				t.Fatal("corrupt allocator snapshot restored")
			}
		})
	}
}
func TestOperationAllocationRestartAndRestoreEpoch(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot-%t", snapshot), func(t *testing.T) {
			config := testConfig(t)
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			r, c := allocatedCommand(t, s, reservationCommand(t, s, "retained", time.Now().UTC()))
			active := submit(t, s, c)[0]
			if active.Err != nil {
				t.Fatal(active.Err)
			}
			pending, unactivated := allocatedCommand(t, s, reservationCommand(t, s, "pending", time.Now().UTC()))
			if snapshot {
				if err := s.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Operation(pending.ID)
			if err != nil || got.State != "reserved" || got.CommittedIndex != 0 {
				t.Fatal(got, err)
			}
			if found, ok := s.PendingOperationForVersion(active.Catalog.Key, active.Catalog.UID, "", active.Catalog.Revision); !ok || found.ID != r.ID {
				t.Fatal("index not rebuilt", found, ok)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := MarkRestored(config.Storage.Directory, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			admin := openAuthenticationAdmin(t, config)
			state, err := admin.Authentication()
			if err != nil {
				t.Fatal(err)
			}
			provision := AuthenticationCommand{Mode: "provision", Epoch: state.Epoch, ExpectedEpoch: state.Epoch, ExpectedRevision: state.Revision, Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(), Principals: authenticationBootstrap().Principals}
			if _, err := admin.CommitAuthentication(context.Background(), provision); err != nil {
				t.Fatal(err)
			}
			if err := admin.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, id := range []string{r.ID, pending.ID} {
				if _, err := s.Operation(id); !errors.Is(err, ErrOperationExpired) {
					t.Fatal("old epoch survived restore", id, err)
				}
			}
			if got := submit(t, s, unactivated)[0]; !errors.Is(got.Err, ErrOperationExpired) {
				t.Fatal("restored pending input executed", got.Err)
			}
			next, _ := allocatedCommand(t, s, reservationCommand(t, s, "after-reset", time.Now().UTC()))
			epoch, seq, _ := ParseOperationHandle(next.ID)
			old, _, _ := ParseOperationHandle(r.ID)
			if epoch == old || seq != 1 {
				t.Fatal("restore did not reset independent allocation epoch", next.ID)
			}
			page, err := s.History().Page("pending", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range page.Events {
				if e.Operation != nil && e.Operation.ID == pending.ID && e.Reason == "explicit_restore" && e.Operation.CommittedIndex == 0 {
					found = true
				}
			}
			if !found {
				t.Fatal("restore omitted reservation cancellation audit")
			}
		})
	}
}
func TestOperationAllocationForcedTermination(t *testing.T) {
	const helper = "CPRA_OPERATION_ALLOCATION_HELPER"
	if directory := os.Getenv(helper); directory != "" {
		config := testConfig(t)
		config.Storage.Directory = directory
		s, err := Open(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.ReserveOperation(context.Background(), reservationCommand(t, s, "crash-reservation", time.Now().UTC()))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(r.ID)
		select {}
	}
	config := testConfig(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOperationAllocationForcedTermination$")
	cmd.Env = append(os.Environ(), helper+"="+config.Storage.Directory)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	var id string
	select {
	case id = <-line:
	case <-time.After(15 * time.Second):
		t.Fatal("allocation helper timed out")
	}
	if _, _, err := ParseOperationHandle(id); err != nil {
		t.Fatal("invalid helper result", id, stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Operation(id)
	if err != nil || r.State != "reserved" || r.CommittedIndex != 0 {
		t.Fatal("committed allocation lost after SIGKILL", r, err)
	}
	if _, ok, _ := s.CatalogGet(CatalogKey{Kind: "Monitor", ID: "crash-reservation"}); ok {
		t.Fatal("replay activated reserved input")
	}
	next, err := s.ReserveOperation(context.Background(), reservationCommand(t, s, "next", time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	e, seq, _ := ParseOperationHandle(next.ID)
	old, _, _ := ParseOperationHandle(id)
	if e != old || seq != 2 {
		t.Fatal("committed highwater lost", next.ID)
	}
}
func TestReservedControlAndRecoveryUseSeparateVersions(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := time.Now().UTC()
	c := controlRequest(m, "snooze", uuid.NewString(), at)
	r, c := allocatedCommand(t, s, c)
	result := submit(t, s, c)[0]
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Monitor.ControlRevision == r.ID || result.Monitor.ControlRevision != c.Control.Revision {
		t.Fatal("control version became operation ID")
	}
	unpause := controlRequest(*result.Monitor, "unsnooze", uuid.NewString(), at.Add(time.Second))
	_, unpause = allocatedCommand(t, s, unpause)
	result = submit(t, s, unpause)[0]
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if got, err := s.Operation(r.ID); err != nil || got.Outcome != "superseded" {
		t.Fatal("separate control receipt not superseded", got, err)
	}
	manual := manualCommand(*result.Monitor, g, uuid.NewString(), at.Add(2*time.Second))
	manual.ManualRecovery.Revision = manual.ManualRecovery.OperationID
	before := *manual.ManualRecovery
	reserved, manual := allocatedCommand(t, s, manual)
	if !reflect.DeepEqual(before.Limits, manual.ManualRecovery.Limits) {
		t.Fatal("reservation mutated caller limits")
	}
	results, err := s.RequestRecovery(context.Background(), manual)
	if err != nil || results[0].Err != nil {
		t.Fatal("manual activation", err, results[0].Err)
	}
	if results[0].Operation.ID != reserved.ID || results[0].Operation.NewVersion != before.Revision {
		t.Fatal("manual identities conflated", results[0].Operation)
	}
}

func TestOperationReservationQuotaAndBoundedRetirement(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	// One active receipt is deliberately mixed with abandoned reservations.
	r, target := allocatedCommand(t, s, reservationCommand(t, s, "active-quota", at))
	active := submit(t, s, target)[0]
	if active.Err != nil {
		t.Fatal(active.Err)
	}
	base := reservationCommand(t, s, "unactivated-quota", at)
	pending, _, err := operationDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	allocation := OperationAllocation{Epoch: uuid.NewString(), Reservation: pending}
	remaining := maxPendingCatalogOperations - 1
	for remaining > 0 {
		n := min(256, remaining)
		commands := make([]Command, n)
		for i := range commands {
			commands[i] = Command{Kind: "operation_reserve", At: at, OperationAllocation: &allocation}
		}
		for _, result := range submit(t, s, commands...) {
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		}
		remaining -= n
	}
	if _, err := s.ReserveOperation(context.Background(), base); !errors.Is(err, ErrCatalogBusy) {
		t.Fatal("unbounded pending reservations", err)
	}
	s.fsm.mu.RLock()
	before := s.fsm.image.OperationHighWater
	s.fsm.mu.RUnlock()
	results, err := s.ExpireOperationReservations(context.Background(), at.Add(OperationReservationLifetime))
	if err != nil || len(results) != 1 || len(results[0].Events) != operationRetirementBatch {
		t.Fatal("retirement not bounded", err)
	}
	s.fsm.mu.RLock()
	count, high := s.fsm.pendingOperationCount(), s.fsm.image.OperationHighWater
	s.fsm.mu.RUnlock()
	if count != maxPendingCatalogOperations-operationRetirementBatch || high != before {
		t.Fatal("retirement changed allocation identity", count, high)
	}
	if got, err := s.Operation(r.ID); err != nil || got.State != "committed" {
		t.Fatal("active operation expired", got, err)
	}
	// New allocation reclaims at most one further batch and issues a new sequence.
	base.At = at.Add(OperationReservationLifetime)
	if _, err := s.ReserveOperation(context.Background(), base); err != nil {
		t.Fatal("retired capacity was not reusable", err)
	}
	s.fsm.mu.RLock()
	count = s.fsm.pendingOperationCount()
	high = s.fsm.image.OperationHighWater
	s.fsm.mu.RUnlock()
	if count != maxPendingCatalogOperations-2*operationRetirementBatch+1 || high != before+1 {
		t.Fatal(count, high)
	}
}

func TestOperationReservationActivationUsesFreshEligibility(t *testing.T) {
	s := openCatalogMemory(t)
	m, g := manualFixture(t, s)
	at := time.Now().UTC()
	// A maintenance window begins after reservation but before activation.
	m.Policy.Maintenance = []MaintenanceWindow{{Start: at.Add(time.Minute), End: at.Add(2 * time.Minute)}}
	m = *mustResult(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: g, At: at}).Monitor
	manual := manualCommand(m, g, uuid.NewString(), at)
	manual.ManualRecovery.Revision = manual.ManualRecovery.OperationID
	r, manual := allocatedCommand(t, s, manual)
	manual.At = at.Add(90 * time.Second)
	results, err := s.RequestRecovery(context.Background(), manual)
	if err != nil || !errors.Is(results[0].Err, ErrRecoveryIneligible) {
		t.Fatal("stale reservation time bypassed maintenance", err, results)
	}
	if got, err := s.Operation(r.ID); err != nil || got.CommittedIndex != 0 || got.Outcome != "activation_rejected" {
		t.Fatal(got, err)
	}
	got, _ := s.Get(m.ID)
	if len(got.Actions) != 0 || got.InterventionAttempts != 0 {
		t.Fatal("rejected recovery consumed action budget", got)
	}
}

func TestOperationRejectedReceiptCannotClaimCommit(t *testing.T) {
	at := time.Now().UTC()
	for _, outcome := range []string{"activation_rejected", "reservation_expired"} {
		r := OperationReceipt{ID: operationHandle(uuid.NewString(), 1), Key: CatalogKey{Kind: "Monitor", ID: "monitor"}, UID: uuid.NewString(), NewVersion: uuid.NewString(), Generation: 1, Actor: "oncall", At: at, UpdatedAt: at, State: "failed", Outcome: outcome}
		if err := r.validate(); err != nil {
			t.Fatal(err)
		}
		r.CommittedIndex = 1
		if err := r.validate(); err == nil {
			t.Fatal("zero-change failure claimed target commit", outcome)
		}
	}
}

func TestOperationReservationFreezesDeploymentRecoveryLimits(t *testing.T) {
	config := testConfig(t)
	config.Storage.Mode = "memory"
	config.ManualRecovery.MinimumInterval = 2 * time.Minute
	config.ManualRecovery.PerHour = 1
	s, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, g := manualFixture(t, s)
	at := time.Now().UTC()
	command := manualCommand(m, g, uuid.NewString(), at)
	command.ManualRecovery.Revision = command.ManualRecovery.OperationID
	command.ManualRecovery.Limits.MinimumInterval = time.Second
	command.ManualRecovery.Limits.PerHour = 100
	before := *command.ManualRecovery
	_, command = allocatedCommand(t, s, command)
	if command.ManualRecovery.Limits != before.Limits {
		t.Fatal("caller limit fields were mutated")
	}
	results, err := s.RequestRecovery(context.Background(), command)
	if err != nil || results[0].Err != nil {
		t.Fatal("reserve/activation froze different effective limits", err, results)
	}
	m = finishManualFailure(t, s, *results[0].Monitor, g, at.Add(time.Second))
	next := manualCommand(m, g, uuid.NewString(), at.Add(time.Minute))
	next.ManualRecovery.Revision = next.ManualRecovery.OperationID
	next.ManualRecovery.Limits = before.Limits
	r, next := allocatedCommand(t, s, next)
	results, err = s.RequestRecovery(context.Background(), next)
	if err != nil || !errors.Is(results[0].Err, ErrRecoveryRateLimited) {
		t.Fatal("caller bypassed deployment limits", err, results)
	}
	if got, err := s.Operation(r.ID); err != nil || got.CommittedIndex != 0 {
		t.Fatal("rate rejection claimed target commit", got, err)
	}
}

func TestOperationReservationMaintenanceOnlyWritesExpiredWork(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	r, _ := allocatedCommand(t, s, reservationCommand(t, s, "maintenance-reservation", at))
	before := s.Status().CommittedIndex
	if err := s.maintainOperationReservations(at); err != nil {
		t.Fatal(err)
	}
	if after := s.Status().CommittedIndex; after != before {
		t.Fatal("empty maintenance committed work", before, after)
	}
	if err := s.maintainOperationReservations(r.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Operation(r.ID); !errors.Is(err, ErrOperationExpired) {
		t.Fatal("maintenance did not retire inactive reservation", got, err)
	}
	s.fsm.mu.RLock()
	count := s.fsm.pendingOperationCount()
	s.fsm.mu.RUnlock()
	if count != 0 {
		t.Fatal("maintenance did not free quota", count)
	}
	if after := s.Status().CommittedIndex; after <= before {
		t.Fatal("maintenance retirement was not durable")
	}
}
