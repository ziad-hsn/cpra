package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/encryptionsetup"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type mainCollectionObserver struct {
	done  chan struct{}
	err   error
	ready bool
}

func (o mainCollectionObserver) Done() <-chan struct{} { return o.done }
func (o mainCollectionObserver) Err() error            { return o.err }
func (o mainCollectionObserver) Ready() bool           { return o.ready }

type mainCollectionJoiner struct {
	done chan struct{}
	err  error
}

func (j mainCollectionJoiner) Done() <-chan struct{}      { return j.done }
func (j mainCollectionJoiner) Wait(context.Context) error { return j.err }

func TestMainCollectionShutdownDistinguishesFailureFromOwnership(t *testing.T) {
	fatal := errors.New("joined compiler failure")
	for _, tc := range []struct {
		name   string
		closed bool
		err    error
	}{
		{"joined-fatal", true, fatal},
		{"unjoined-deadline", false, context.DeadlineExceeded},
		{"deadline-raced-with-join", true, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := mainCollectionJoiner{done: make(chan struct{}), err: tc.err}
			if tc.closed {
				close(owner.done)
			}
			joined, err := joinRuntimeCollection(context.Background(), owner)
			if joined != tc.closed || !errors.Is(err, tc.err) {
				t.Fatal("shutdown confused reported failure with live dependency ownership", joined, err)
			}
		})
	}
}

func TestMainCollectionInitializationJoinsWaitOnCoordinatorFailure(t *testing.T) {
	want := errors.New("coordinator failed")
	observer := mainCollectionObserver{done: make(chan struct{}), err: want}
	entered, joined := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- waitRuntimeInitialization(context.Background(), func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			close(joined)
			return ctx.Err()
		}, observer)
	}()
	<-entered
	close(observer.done)
	select {
	case err := <-finished:
		if !errors.Is(err, want) {
			t.Fatal("coordinator failure lost", err)
		}
		select {
		case <-joined:
		default:
			t.Fatal("initialization abandoned controller wait")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("coordinator failure did not release initialization")
	}
}

func TestMainCollectionInitializationNeverReadiesStoppedCoordinator(t *testing.T) {
	observer := mainCollectionObserver{done: make(chan struct{})}
	if err := waitRuntimeInitialization(context.Background(), func(context.Context) error { return nil }, observer); err == nil {
		t.Fatal("controller initialization concealed unavailable coordinator")
	}
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	err := waitRuntimeInitialization(ctx, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		close(joined)
		return ctx.Err()
	}, observer)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("process cancellation lost", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("process cancellation abandoned controller wait")
	}
}

// The stopped fixture uses the same authenticated catalog admission as the
// application. No public Validate route or controller/provider is started here.
func seedMainCollectionValidation(t *testing.T, f mainManagementFixture, claimed bool) persistence.CollectionState {
	t.Helper()
	ctx := t.Context()
	encryption, err := encryptionsetup.Open(ctx, encryptionsetup.Options{StorageMode: f.settings.Storage.Mode,
		DataDirectory: f.settings.Storage.Directory, Encryption: f.settings.Management.Encryption})
	if err != nil {
		t.Fatal(err)
	}
	defer encryption.Close()
	store, err := persistence.Open(ctx, f.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := initializeRuntimeAuthentication(ctx, store, f.settings, f.options); err != nil {
		t.Fatal(err)
	}
	startup, err := management.StartupCatalog(ctx, store, encryption.Sealer(), managementStartupOptions(f.settings, f.options))
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x3b}, commitment.KeyBytes)
	defer clear(key)
	var fingerprint [commitment.MACBytes]byte
	copy(fingerprint[:], bytes.Repeat([]byte{0x72}, commitment.MACBytes))
	raw := []byte(`{"apiVersion":"cpra.io/v2","kind":"Credential","metadata":{"id":"staged-secret"},"spec":{"value":"private-lifecycle-canary"}}`)
	defer clear(raw)
	position := commitment.Position{Ordinal: 1, ID: "Credential/staged-secret",
		Source: commitment.SourcePosition{Token: "source.00000000000000000001", Document: 1, Item: 1}}
	mac, err := commitment.ItemMAC(key, position, raw)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, 1, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if err := acc.Add(position, mac); err != nil {
		t.Fatal(err)
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	prepare := api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(commitment.Format),
		IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])),
		ContentDigest: hex.EncodeToString(digest[:]), ItemCount: 1}
	admit := func(commit func() error) error { return commit() }
	catalog := startup.Catalog
	admission, err := catalog.PrepareCollection(ctx, prepare, "team/oncall", time.Now, admit)
	if err != nil {
		t.Fatal(err)
	}
	op, err := catalog.CreateCollection(ctx, api.OperationCreateRequest{AdmissionTicket: admission.Ticket,
		IdentityFormat: api.OperationCreateRequestIdentityFormat(prepare.IdentityFormat), IdentityKey: prepare.IdentityKey,
		SourceFingerprint: prepare.SourceFingerprint, ContentDigest: prepare.ContentDigest, ItemCount: prepare.ItemCount}, "team/oncall", time.Now, admit)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.UploadCollection(ctx, op.ID, "team/oncall", []management.CollectionUploadItem{{Ordinal: 1,
		Key: persistence.CatalogKey{Kind: "Credential", ID: "staged-secret"}, Source: position.Source.Token,
		SourceDocument: 1, SourceItem: 1, ContentDigest: hex.EncodeToString(mac[:]), Resource: raw}},
		func(key persistence.CatalogKey) bool { return key.Kind == "Credential" && key.ID == "staged-secret" }, time.Now, admit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.RequestCollectionValidation(ctx, op.ID, "team/oncall", time.Now, admit); err != nil {
		t.Fatal(err)
	}
	head, exists, err := store.CollectionGet(op.ID)
	if err != nil || !exists || head.ValidationRequest == nil || head.ValidationRequest.Claim != nil {
		t.Fatal("original unclaimed request absent", err)
	}
	if claimed {
		at := time.Now().UTC()
		results, err := store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{
			Action: "validation_claim", OperationID: head.ID, UploadID: head.UploadID,
			ValidationFence: persistence.CollectionValidationRequestFenceFor(head),
			ValidationClaim: &persistence.CollectionValidationClaim{ID: uuid.NewString(), RunID: uuid.NewString(), At: at}}}})
		if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Collection == nil {
			t.Fatal("fixture claim failed", err)
		}
		head = results[0].Collection.Clone()
	}
	if err := store.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return head
}

func TestMainCollectionLifecycleRecoversRequestsWithoutActivation(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending-first-attempt", true: "abandoned-claim"}[claimed], func(t *testing.T) {
			fixture := newMainManagementFixture(t)
			original := seedMainCollectionValidation(t, fixture, claimed)
			client, stop := startMainManagement(t, fixture)
			want := "validated"
			if claimed {
				want = "interrupted"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			for {
				response, err := client.Operations.Get(ctx, original.ID)
				if err != nil {
					t.Fatal("read original collection", err)
				}
				if response.Data.State == want {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("coordinator did not reach expected original disposition", response.Data.State, want)
				case <-time.After(20 * time.Millisecond):
				}
			}
			stop()
			store, err := persistence.Open(t.Context(), fixture.settings)
			if err != nil {
				t.Fatal("graceful stop retained a live store owner", err)
			}
			defer store.Close()
			receipt, err := store.CollectionReceipt(t.Context(), original.ID, time.Now())
			if err != nil || receipt.Phase != want || receipt.ValidationRequest == nil || receipt.ValidationRequest.ID != original.ValidationRequest.ID {
				t.Fatal("original request outcome did not survive shutdown", err)
			}
			if claimed {
				if receipt.Validation != nil || receipt.ValidationRequest.Claim.ID != original.ValidationRequest.Claim.ID ||
					receipt.ValidationRequest.Interruption == nil || receipt.ValidationRequest.Interruption.Reason != "coordinatorRestarted" {
					t.Fatal("startup replaced abandoned attempt instead of retiring it")
				}
			} else if receipt.Validation == nil || !receipt.Validation.Header.Valid || receipt.Validation.Descriptor.Count != 1 || receipt.ValidationRequest.Claim == nil {
				t.Fatal("pending request did not retain one actual validation result")
			}
			view, err := store.CatalogSnapshot()
			if err != nil || view.Len() != 0 {
				t.Fatal("validation lifecycle activated staged input", err)
			}
		})
	}
}

func TestMainCollectionLifecycleJoinsOnListenerFailure(t *testing.T) {
	fixture := newMainManagementFixture(t)
	seedMainCollectionValidation(t, fixture, false)
	listener, err := net.Listen("tcp", fixture.options.webAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err = runCPRa(ctx, fixture.options, func() { t.Error("failed listener announced readiness") })
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatal("expected real listener failure", err)
	}
	store, err := persistence.Open(t.Context(), fixture.settings)
	if err != nil {
		t.Fatal("early failure left a coordinator/storage owner running", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
