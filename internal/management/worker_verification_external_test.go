//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestWorkerExecutionStartupRejectsUnauthenticatedIntent(t *testing.T) {
	for _, change := range []string{"ciphertext", "missing-key", "identity", "json", "unknown-field", "schema", "profile"} {
		t.Run(change, func(t *testing.T) {
			c, store, intent := workerAssignmentFixture(t)
			prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "ciphertext":
				prepared.Payload.Ciphertext[0] ^= 1
			case "missing-key":
				prepared.Payload.KeyID = "missing-private-key-canary"
			case "identity":
				// Structurally valid scheduling metadata with the original ciphertext.
				prepared.Deadline = prepared.Deadline.Add(time.Second)
			default:
				plain, err := c.sealer.Open(t.Context(), prepared.Binding(c.storeID), prepared.Payload)
				if err != nil {
					t.Fatal(err)
				}
				var payload workerAssignmentPayloadV1
				if err = json.Unmarshal(plain, &payload); err != nil {
					t.Fatal(err)
				}
				clear(plain)
				switch change {
				case "schema":
					payload.Parameters = json.RawMessage(`{"target":123}`)
				case "profile":
					payload.CredentialProfile = "private-profile-canary\n"
				}
				plain, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				if change == "json" {
					clear(plain)
					plain = []byte(`{"identity":"private-json-canary"`)
				}
				if change == "unknown-field" {
					plain = append(plain[:len(plain)-1], []byte(`,"unsupported":"private-field-canary"}`)...)
				}
				prepared.Payload, err = c.sealer.Seal(t.Context(), prepared.Binding(c.storeID), plain)
				clear(plain)
				clear(payload.Parameters)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Persistence accepts structural envelopes; startup supplies the
			// cryptographic/schema verification without dispatching the record.
			if _, err = store.CommitWorkerExecution(t.Context(), prepared); err != nil {
				t.Fatal("structural fixture admission", err)
			}
			reopened, err := NewCatalog(store, c.sealer)
			if err != nil {
				t.Fatal(err)
			}
			if err = reopened.Verify(t.Context()); !errors.Is(err, ErrUnavailable) || strings.Contains(fmt.Sprint(err), "canary") {
				t.Fatal("invalid retained assignment accepted or exposed", err)
			}
			if reopened.Ready() || reopened.verified.Load() || store.Status().Ready {
				t.Fatal("startup opened admission after verification failure")
			}
		})
	}
}

type workerVerificationWrapper struct {
	secureconfig.KeyWrapper
	cancel context.CancelFunc
	opens  int
	wraps  int
}

func (w *workerVerificationWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	if bytes.Contains(aad, []byte("worker-assignment-v1")) {
		w.opens++
		if w.cancel != nil {
			w.cancel()
			return nil, ctx.Err()
		}
	}
	return w.KeyWrapper.Unwrap(ctx, wrapped, aad)
}

func (w *workerVerificationWrapper) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	w.wraps++
	return nil, errors.New("startup unexpectedly sealed data")
}

func TestWorkerExecutionStartupCancellationAndReadOnlyVerification(t *testing.T) {
	c, store, intent := workerAssignmentFixture(t)
	prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.CommitWorkerExecution(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.Get(intent.MonitorID)
	index := store.Status().CommittedIndex
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wrapper := &workerVerificationWrapper{KeyWrapper: inner, cancel: cancel}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Verify(ctx); !errors.Is(err, context.Canceled) || wrapper.opens != 1 || reopened.Ready() || reopened.failed.Load() {
		t.Fatal("assignment cancellation did not remain unavailable/retryable", err, wrapper.opens)
	}
	wrapper.cancel = nil
	if err = reopened.Verify(t.Context()); err != nil || !reopened.Ready() || wrapper.opens != 2 || wrapper.wraps != 0 {
		t.Fatal("read-only verification did not recover from cancellation", err)
	}
	after, _ := store.Get(intent.MonitorID)
	got, ok, err := store.WorkerExecution(t.Context(), intent.ID)
	if err != nil || !ok || !reflect.DeepEqual(saved, got) || !reflect.DeepEqual(before, after) || store.Status().CommittedIndex != index {
		t.Fatal("verification changed committed execution or lifecycle", err)
	}
}

func workerVerificationNativeFixture(t *testing.T) (*Catalog, *persistence.Store, runtimeconfig.Config, persistence.WorkerExecutionIntent) {
	t.Helper()
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	store, err := persistence.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	keys, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(keys)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitAuthentication(t.Context(), collectionOwnerBootstrap(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	createReferenceJobType(t, c, "check")
	intent := workerVerificationIntent(t, c, store, "monitor")
	intent.Scheduled = time.Now().Add(-time.Minute).UTC()
	intent.Deadline = intent.Scheduled.Add(time.Second)
	return c, store, config, intent
}

func workerVerificationIntent(t *testing.T, c *Catalog, store *persistence.Store, id string) persistence.WorkerExecutionIntent {
	t.Helper()
	input := externalReferenceResource("check", false)
	input.Metadata.ID = id
	prepared := prepareReferenceResource(t, c, input)
	if _, err := c.CommitAs(t.Context(), prepared, "team/operator"); err != nil {
		t.Fatal(err)
	}
	r := prepared.mutation.Record
	guard := persistence.CatalogGuard{Conditions: []persistence.CatalogCondition{{Key: r.Key, UID: r.UID, Revision: r.Revision}}}
	m := persistence.Monitor{ID: r.Key.ID, Revision: "startup-config", CatalogUID: r.UID, CatalogRevision: r.Revision, Policy: persistence.Policy{Enabled: true, Interval: time.Minute, Healthy: 1, Unhealthy: 1}}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, Guard: &guard, At: time.Now().UTC()}})
	if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
		t.Fatal("configure", err)
	}
	m, _ = store.Get(m.ID)
	at := time.Now().UTC()
	return persistence.WorkerExecutionIntent{ID: "execution-" + id, Revision: "startup-revision", MonitorID: m.ID, MonitorUID: m.CatalogUID, MonitorRevision: m.Revision, ControlRevision: m.ControlRevision, Category: "check", Generation: 1, Source: r.Key, SourceUID: r.UID, SourceRevision: r.Revision, JobType: r.JobTypeReferences[0], Guard: guard, Scheduled: at, Deadline: at.Add(5 * time.Second)}
}

func TestWorkerExecutionStartupAuthenticatesPastFirstPage(t *testing.T) {
	c, store := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	for n := 0; n < 101; n++ {
		intent := workerVerificationIntent(t, c, store, fmt.Sprintf("monitor-%03d", n))
		prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
		if err != nil {
			t.Fatal(err)
		}
		if n == 100 {
			prepared.Payload.Ciphertext[0] ^= 1
		}
		if _, err = store.CommitWorkerExecution(t.Context(), prepared); err != nil {
			t.Fatal(err)
		}
	}
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &workerVerificationWrapper{KeyWrapper: inner}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Verify(t.Context()); !errors.Is(err, ErrUnavailable) || wrapper.opens != 101 || reopened.verified.Load() {
		t.Fatal("startup skipped later retained ciphertext", err, wrapper.opens)
	}
}

func TestWorkerExecutionStartupNativeRetainsOriginalExpiredIntent(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(snapshot), func(t *testing.T) {
			c, store, config, intent := workerVerificationNativeFixture(t)
			prepared, err := c.PrepareWorkerExecution(t.Context(), intent)
			if err != nil {
				t.Fatal(err)
			}
			// Replay a valid historical observation through the actual FSM. It
			// is already expired at wall time; no simulated endurance is claimed.
			results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "local_session", ExecutorSession: "historical-owner", At: intent.Scheduled}})
			if err != nil || len(results) != 1 || results[0].Err != nil {
				t.Fatal("historical owner", err)
			}
			command := persistence.Command{Kind: "worker_execution", At: intent.Scheduled}
			command.WorkerExecution = &persistence.WorkerExecutionCommand{Intent: prepared, OwnerEpoch: "historical-owner"}
			results, err = store.Submit(t.Context(), []persistence.Command{command})
			if err != nil || len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
				t.Fatal("historical intent", err, results)
			}
			saved := results[0].WorkerExecution.Clone()
			removal, err := c.PrepareDelete(t.Context(), "Monitor", intent.MonitorID, intent.SourceRevision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.CommitAs(t.Context(), removal, "team/operator"); err != nil {
				t.Fatal(err)
			}
			job, err := c.GetJobType(t.Context(), intent.JobType.JobTypeID)
			if err != nil {
				t.Fatal(err)
			}
			job.Spec.Version = "2"
			job.Spec.ParameterSchema = json.RawMessage(`{"const":"different-contract"}`)
			commitTestJobType(t, c, job, job.Metadata.ResourceVersion, false)
			if snapshot {
				if err = store.Snapshot(); err != nil {
					t.Fatal(err)
				}
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, err := persistence.Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedStore.Close() })
			reopened, err := NewCatalog(reopenedStore, c.sealer)
			if err != nil {
				t.Fatal(err)
			}
			index := reopenedStore.Status().CommittedIndex
			if err = reopened.Verify(t.Context()); err != nil || !reopened.Ready() {
				t.Fatal("valid original assignment blocked startup", err)
			}
			got, ok, err := reopenedStore.WorkerExecution(t.Context(), intent.ID)
			if err != nil || !ok || !reflect.DeepEqual(got, saved) || reopenedStore.Status().CommittedIndex != index {
				t.Fatal("startup rewrote retained execution", err)
			}
			page, err := reopenedStore.WorkerExecutionsReady(t.Context(), intent.JobType, "", 100)
			if err != nil || len(page.Items) != 0 {
				t.Fatal("startup verification granted stale work", err)
			}
		})
	}
}
