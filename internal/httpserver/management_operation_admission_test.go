package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func requireAllocatedOperation(t *testing.T, id string, distinct ...string) {
	t.Helper()
	if _, _, err := persistence.ParseOperationHandle(id); err != nil {
		t.Fatalf("not a canonical allocated operation: %q", id)
	}
	for _, other := range distinct {
		if id == other {
			t.Fatal("operation identity reused a resource, control, or execution identity")
		}
	}
}

func admissionCountIs(value *int64, expected int64) bool {
	return value != nil && *value == expected
}

func admissionFlagIs(value *bool, expected bool) bool {
	return value != nil && *value == expected
}

func TestManagementOperationAdmissionIdentitiesThroughTLSAndSDK(t *testing.T) {
	f := newManagementFixture(t, true)
	ctx := context.Background()
	secret := "operation-identity-secret-not-returned"
	created, err := f.sdk.Credentials.Create(ctx, api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "private"}, Spec: api.CredentialSpec{Value: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	requireAllocatedOperation(t, created.OperationID, created.ResourceVersion, created.Data.Metadata.UID)
	if created.ResourceVersion != created.Data.Metadata.ResourceVersion || created.Data.Spec.Value != nil {
		t.Fatal("resource response lost its own version or exposed a credential")
	}
	op, err := f.sdk.Operations.Get(ctx, created.OperationID)
	if err != nil || op.Data.ID != created.OperationID || op.Data.State != "committed" || !admissionCountIs(op.Data.Committed, 1) || !admissionCountIs(op.Data.Applied, 0) || len(op.Data.Items) != 1 || op.Data.Items[0].NewVersion != created.ResourceVersion {
		t.Fatalf("resource save/application receipt: %+v %v", op, err)
	}
	resource, monitor, guard := configureControlMonitor(t, f, "managed")
	monitor = controlPulse(t, f, monitor, guard, "failure")
	ack, err := f.sdk.Incidents.Acknowledge(ctx, monitor.IncidentID, api.ControlRequest{Revision: monitor.IncidentRevision, Note: "Investigating"})
	if err != nil {
		t.Fatal(err)
	}
	requireAllocatedOperation(t, ack.OperationID, ack.Data.Revision, resource.Metadata.ResourceVersion, monitor.Revision, created.OperationID)
	if ack.ResourceVersion != ack.Data.Revision || ack.Data.Revision == monitor.IncidentRevision || ack.Data.AcknowledgedBy != "operator" {
		t.Fatal("triage revision or authenticated actor was lost")
	}
	snooze, err := f.sdk.Monitors.Snooze(ctx, monitor.ID, api.ControlRequest{Revision: monitor.ControlRevision, Duration: "1m", Reason: "Planned maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.sdk.Monitors.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireAllocatedOperation(t, snooze.OperationID, current.Data.Status.ControlRevision, resource.Metadata.ResourceVersion, monitor.Revision, ack.OperationID)
	if snooze.Data.ID != snooze.OperationID || snooze.Data.State != "committed" || !admissionCountIs(snooze.Data.Committed, 1) || !admissionCountIs(snooze.Data.Applied, 0) || current.Data.Metadata.ResourceVersion != resource.Metadata.ResourceVersion || current.Data.Status.ControlRevision == monitor.ControlRevision {
		t.Fatal("snooze operation confused catalog/control identity or claimed owner application")
	}
	for _, control := range []struct{ id, revision string }{{ack.OperationID, ack.Data.Revision}, {snooze.OperationID, current.Data.Status.ControlRevision}} {
		got, err := f.sdk.Operations.Get(ctx, control.id)
		if err != nil || got.Data.ID != control.id || !admissionCountIs(got.Data.Committed, 1) || !admissionCountIs(got.Data.Applied, 0) || len(got.Data.Items) != 1 || got.Data.Items[0].NewVersion != control.revision {
			t.Fatalf("independent control receipt: %+v %v", got, err)
		}
	}
}

func TestManagementCompetingPreparedAdmissionReceiptThroughTLS(t *testing.T) {
	f := newManagementFixture(t, true)
	ctx := context.Background()
	// Freeze both valid creates before admitting either. This deterministically
	// reaches the real post-prepare CAS conflict without a production HTTP hook.
	first, err := f.catalog.Prepare(ctx, managementResource("Credential", "competing", api.CredentialSpec{Value: api.Pointer("original-private-value")}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := f.catalog.Prepare(ctx, managementResource("Credential", "competing", api.CredentialSpec{Value: api.Pointer("rejected-private-value")}), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID() != "" || stale.OperationID() != "" {
		t.Fatal("pure preparation allocated a receipt")
	}
	saved, err := f.catalog.CommitAs(ctx, first, "operator")
	if err != nil {
		t.Fatal(err)
	}
	before, ok, err := f.server.cfg.Store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "competing"})
	if err != nil || !ok {
		t.Fatal("saved target unavailable", err)
	}
	_, conflict := f.catalog.CommitAs(ctx, stale, "operator")
	if !errors.Is(conflict, persistence.ErrCatalogConflict) {
		t.Fatal("competing prepared create was not rejected", conflict)
	}
	rejectedID := stale.OperationID()
	requireAllocatedOperation(t, rejectedID, saved.Operation.ID, saved.Resource.Metadata.ResourceVersion)
	page, err := f.sdk.Operations.Get(ctx, rejectedID)
	if err != nil || page.Data.ID != rejectedID || page.Data.State != "failed" || !admissionCountIs(page.Data.Committed, 0) || !admissionCountIs(page.Data.Applied, 0) || !admissionFlagIs(page.Data.Validated, false) || len(page.Data.Items) != 1 || page.Data.Items[0].Outcome != "activation_rejected" || !admissionFlagIs(page.Data.Items[0].Committed, false) || !admissionFlagIs(page.Data.Items[0].Applied, false) {
		t.Fatalf("rejected receipt is not truthful: %+v %v", page, err)
	}
	response, raw := f.request(t, "GET", "/api/v2/operations/"+rejectedID, managementReaderToken, nil, nil)
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte(`"committed":0`)) || !bytes.Contains(raw, []byte(`"applied":0`)) || bytes.Contains(raw, []byte("private-value")) {
		t.Fatal("receipt omitted exact progress or exposed a private payload")
	}
	after, ok, err := f.server.cfg.Store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "competing"})
	if err != nil || !ok || !reflect.DeepEqual(before, after) {
		t.Fatal("failed admission changed the saved encrypted target", err)
	}
	current, err := f.sdk.Credentials.Get(ctx, "competing")
	if err != nil || current.Data.Metadata.ResourceVersion != saved.Resource.Metadata.ResourceVersion || current.Data.Metadata.UID != saved.Resource.Metadata.UID || current.Data.Spec.Value != nil {
		t.Fatal("original saved resource did not survive the rejected admission", err)
	}
	// Exercise the actual public error mapping over TLS using the conflict
	// returned above. Reads above use the normal authenticated production mux;
	// this writer fixture isolates error transport, not concurrent HTTP routing.
	client, capture, calls := admissionErrorClient(t, conflict, rejectedID)
	_, err = client.Credentials.Create(ctx, api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "competing"}, Spec: api.CredentialSpec{Value: api.Pointer("never-submitted-by-error-fixture")}})
	var problem *cpra.Error
	if !errors.Is(err, cpra.ErrConflict) || errors.Is(err, cpra.ErrAmbiguous) || !errors.As(err, &problem) || problem.OperationID != rejectedID || calls.Load() != 1 {
		t.Fatal("known rejection lost its original handle or retried", err)
	}
	wire := <-capture
	if wire.status != 412 || wire.header.Get("X-Operation-ID") != rejectedID || wire.header.Get("X-CPRa-Admission") == "committed" {
		t.Fatal("conflict response falsely claimed committed admission")
	}
}

type admissionErrorWire struct {
	header http.Header
	status int
	body   []byte
}

type admissionCaptureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *admissionCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *admissionCaptureWriter) Write(raw []byte) (int, error) {
	w.body.Write(raw)
	return w.ResponseWriter.Write(raw)
}

// Uses the production error encoder and a real TLS connection; it does not
// simulate a storage failure or claim to exercise allocator failure injection.
func admissionErrorClient(t *testing.T, failure error, previousHandle string) (*cpra.Client, <-chan admissionErrorWire, *atomic.Int64) {
	t.Helper()
	captures := make(chan admissionErrorWire, 4)
	requests := new(atomic.Int64)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		defer r.Body.Close()
		writer := &admissionCaptureWriter{ResponseWriter: w}
		managementHeaders(writer)
		if previousHandle != "" {
			writer.Header().Set("X-Operation-ID", previousHandle)
		}
		writeManagementError(writer, failure)
		captures <- admissionErrorWire{header: writer.Header().Clone(), status: writer.status, body: bytes.Clone(writer.body.Bytes())}
	}))
	t.Cleanup(s.Close)
	client, err := cpra.New(cpra.Config{BaseURL: s.URL, AuthToken: managementOperatorToken, HTTPClient: s.Client(), ReadAttempts: 3, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client, captures, requests
}

func TestManagementAllocationUnconfirmedErrorInteroperatesWithSDK(t *testing.T) {
	private := "private-backend-address-and-token-marker"
	for _, tc := range []struct {
		name           string
		failure        error
		previousHandle string
		status         int
		notAdmitted    bool
	}{
		{"allocation_unconfirmed", fmt.Errorf("%w: %s", persistence.ErrOperationAllocationUnconfirmed, private), private, 503, true},
		{"generic_internal_failure", errors.New(private), "", 500, false},
		{"generic_unavailable", fmt.Errorf("%w: %s", context.DeadlineExceeded, private), "op.633b1c09-2dfa-45fb-8a36-b415db0241a4.00000000000000000001", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, capture, calls := admissionErrorClient(t, tc.failure, tc.previousHandle)
			response, err := client.Credentials.Create(context.Background(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "never-submitted"}, Spec: api.CredentialSpec{Value: api.Pointer(private)}})
			if err == nil || calls.Load() != 1 || errors.Is(err, cpra.ErrNotAdmitted) != tc.notAdmitted || errors.Is(err, cpra.ErrAmbiguous) == tc.notAdmitted {
				t.Fatal("SDK conflated allocator non-admission with mutation uncertainty, or retried", err)
			}
			wire := <-capture
			if wire.status != tc.status || wire.header.Get("Content-Type") != "application/problem+json" || wire.header.Get("Cache-Control") != "no-store" || wire.header.Get("X-Content-Type-Options") != "nosniff" || wire.header.Get("X-Request-ID") == "" {
				t.Fatal("production error envelope lost its strict HTTP contract")
			}
			encodedHeaders, _ := json.Marshal(wire.header)
			if bytes.Contains(wire.body, []byte(private)) || bytes.Contains(encodedHeaders, []byte(private)) || strings.Contains(err.Error(), private) {
				t.Fatal("production error mapping leaked private backend/request values")
			}
			var problem api.Problem
			if api.StrictDecode(wire.body, &problem) != nil || problem.Type != "about:blank" || problem.Status != int64(tc.status) || problem.Title == "" {
				t.Fatal("production problem is malformed")
			}
			if tc.notAdmitted {
				if len(wire.header.Values("X-CPRa-Admission")) != 1 || wire.header.Get("X-CPRa-Admission") != "not-submitted" || len(wire.header.Values("X-Operation-ID")) != 0 || problem.Code != "operationAllocationUnconfirmed" || response == nil || response.OperationID != "" {
					t.Fatal("non-admission error retained a handle or lost its exact disposition")
				}
			} else if response == nil || response.OperationID != tc.previousHandle || wire.header.Get("X-CPRa-Admission") == "not-submitted" {
				t.Fatal("generic server failure fabricated non-admission or lost an existing handle")
			}
		})
	}
}
