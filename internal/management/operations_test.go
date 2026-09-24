package management

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestReservedAndRejectedReceiptsDoNotClaimResourceCommit(t *testing.T) {
	for _, state := range []string{"reserved", "failed"} {
		outcome := "reserved"
		if state == "failed" {
			outcome = "activation_rejected"
		}
		receipt := persistence.OperationReceipt{ID: "op.633b1c09-2dfa-45fb-8a36-b415db0241a4.00000000000000000001", Key: persistence.CatalogKey{Kind: "Monitor", ID: "service"},
			UID: "original-incarnation", NewVersion: "desired-version", State: state, Outcome: outcome, At: time.Now()}
		view := operationView(receipt)
		if (view.Committed == nil || *view.Committed != 0) || (view.Applied == nil || *view.Applied != 0) || (view.Validated == nil || *view.Validated) || len(view.Items) != 1 || (view.Items[0].Committed == nil || *view.Items[0].Committed) || (view.Items[0].Applied == nil || *view.Items[0].Applied) {
			t.Fatalf("%s receipt claimed active configuration: %+v", state, view)
		}
		if view.Items[0].NewVersion != receipt.NewVersion || view.ID != receipt.ID {
			t.Fatal("receipt lost independent handle and desired-version identities")
		}
	}
}

func TestManagementReceiptDistinguishesSaveFromApplication(t *testing.T) {
	c, _ := testCatalog(t)
	value := "do-not-expose-this-secret-in-operation"
	prepared, err := c.Prepare(context.Background(), resource("Credential", "private", api.CredentialSpec{Value: &value}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.OperationID() != "" {
		t.Fatal("pure preparation allocated an operation handle")
	}
	result, err := c.CommitAs(context.Background(), prepared, "team/oncall")
	if err != nil {
		t.Fatal(err)
	}
	id := prepared.OperationID()
	if _, _, err := persistence.ParseOperationHandle(id); err != nil || id == result.Resource.Metadata.ResourceVersion {
		t.Fatal("operation handle lacks an independent allocated identity", err)
	}
	if result.Operation.ID != id || result.Operation.State != "committed" || result.Operation.Applied == nil || *result.Operation.Applied != 0 || result.Operation.Committed == nil || *result.Operation.Committed != 1 {
		t.Fatalf("save misreported application: %+v", result.Operation)
	}
	if err := c.CompleteOperation(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteOperation(context.Background(), id, true); err != nil {
		t.Fatal("identical completion was not idempotent", err)
	}
	if err := c.CompleteOperation(context.Background(), id, false); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatal("contradictory completion overwritten", err)
	}
	got, err := c.Operation(context.Background(), id)
	if err != nil || got.State != "completed" || got.Applied == nil || *got.Applied != 1 {
		t.Fatal("missing applied receipt", err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), value) || strings.Contains(string(raw), "private-runtime") {
		t.Fatal("operation leaked configuration")
	}
}

func TestConflictingAdmissionRetainsOriginalHandleWithZeroTargetCommits(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	first, err := c.Prepare(ctx, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("original-private-value")}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := c.Prepare(ctx, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("stale-private-value")}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID() != "" || stale.OperationID() != "" {
		t.Fatal("preparation issued executable operation identities")
	}
	committed, err := c.CommitAs(ctx, first, "original-operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitAs(ctx, stale, "other-operator"); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatal("stale preflight overwrote a newly active resource", err)
	}
	firstEpoch, firstSequence, err := persistence.ParseOperationHandle(committed.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	epoch, sequence, err := persistence.ParseOperationHandle(stale.OperationID())
	if err != nil || epoch != firstEpoch || sequence != firstSequence+1 {
		t.Fatal("known rejection lost its distinct issued handle", err)
	}
	receipt, err := c.Operation(ctx, stale.OperationID())
	if err != nil || receipt.State != "failed" || (receipt.Committed == nil || *receipt.Committed != 0) || (receipt.Applied == nil || *receipt.Applied != 0) || len(receipt.Items) != 1 || receipt.Items[0].Outcome != "activation_rejected" {
		t.Fatal("rejected admission pretended to change a resource", receipt, err)
	}
	raw, _ := json.Marshal(receipt)
	if strings.Contains(string(raw), "private-value") || !strings.Contains(string(raw), `"committed":0`) || !strings.Contains(string(raw), `"applied":0`) {
		t.Fatal("receipt leaked input or omitted known-zero progress")
	}
	current, err := c.Get(ctx, "Credential", "key")
	if err != nil || current.Metadata.ResourceVersion != committed.Resource.Metadata.ResourceVersion {
		t.Fatal("failed admission changed the original target", err)
	}
	before := store.Status().CommittedIndex
	if _, err := c.CommitAs(ctx, stale, "other-operator"); !errors.Is(err, ErrValidation) || store.Status().CommittedIndex != before || stale.OperationID() != receipt.ID {
		t.Fatal("repeating the prepared command allocated or executed another operation", err)
	}
}

func TestMonitorAdmissionUsesRuntimeIdentityAndSettings(t *testing.T) {
	c, store := testCatalog(t)
	valid := api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}}
	for _, id := range []string{"team service", "a/b", "サービス", strings.Repeat("x", 129), "-leading"} {
		if _, err := c.Prepare(context.Background(), resource("Monitor", id, valid), "", true); !errors.Is(err, ErrValidation) {
			t.Fatalf("unrepresentable monitor id accepted: %v", err)
		}
	}
	for _, spec := range []api.MonitorSpec{
		{Check: api.CheckSpec{Interval: "-1s", Timeout: "5s", Driver: valid.Check.Driver}},
		{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"not-a-url"}`)}}},
		{Check: valid.Check, Maintenance: api.Pointer([]api.MaintenanceWindow{{Cron: api.Pointer("invalid cron"), Duration: api.Pointer("1h")}})},
	} {
		if _, err := c.Prepare(context.Background(), resource("Monitor", "service", spec), "", true); !errors.Is(err, ErrValidation) {
			t.Fatal("unexecutable configuration committed", err)
		}
	}
	view, err := store.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("rejected resource activated configuration", err)
	}
}

func TestCredentialRotationValidatesEveryResolvedConsumer(t *testing.T) {
	c, store := testCatalog(t)
	value := "https://example.test/notify"
	credential := createResource(t, c, resource("Credential", "shared", api.CredentialSpec{Value: &value}))
	createResource(t, c, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "shared"})}))
	if _, err := c.PreparePatch(context.Background(), "Credential", "shared", credential.Metadata.ResourceVersion, []byte(`{"spec":{"value":"secret-but-invalid-url"}}`)); !errors.Is(err, ErrValidation) || strings.Contains(err.Error(), "secret-but-invalid-url") {
		t.Fatal("invalid shared rotation not rejected safely", err)
	}
	record, ok, err := store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "shared"})
	if err != nil || !ok || record.Revision != credential.Metadata.ResourceVersion {
		t.Fatal("failed rotation changed credential", err)
	}
}
