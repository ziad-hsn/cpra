package management

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestPreparedRecoveryCannotCrossConfigurationAdmission(t *testing.T) {
	c, store := testCatalog(t)
	ctx := context.Background()
	original := createResource(t, c, controlResource("m"))
	m := configureFacadeControl(t, c, store, original)
	p, err := c.PrepareAction(ctx, "recover", m.ID, api.ControlRequest{Revision: m.ControlRevision, Reason: "Investigated target"})
	if err != nil {
		t.Fatal(err)
	}
	patch, err := c.PreparePatch(ctx, "Monitor", m.ID, original.Metadata.ResourceVersion, []byte(`{"spec":{"enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitAs(ctx, patch, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitActionAs(ctx, p, "operator"); !errors.Is(err, persistence.ErrCatalogDependency) {
		t.Fatalf("prepared target changed: %v", err)
	}
	if _, err := c.CommitActionAs(ctx, p, "operator"); !errors.Is(err, persistence.ErrControlInvalid) {
		t.Fatal("preparation reused", err)
	}
	after, _ := store.Get(m.ID)
	if len(after.Actions) != 0 {
		t.Fatal("stale preparation queued action")
	}
}
func TestActionPreparationRejectsInvalidAuditAndUnrelatedFields(t *testing.T) {
	c, store := testCatalog(t)
	m := configureFacadeControl(t, c, store, createResource(t, c, controlResource("m")))
	base := api.ControlRequest{Revision: m.ControlRevision, Reason: "Investigated"}
	for _, mutate := range []func(*api.ControlRequest){func(r *api.ControlRequest) { r.Reason = " " }, func(r *api.ControlRequest) { r.Reason = strings.Repeat("é", 2049) }, func(r *api.ControlRequest) { r.Reason = "bad\rreason" }, func(r *api.ControlRequest) { r.Note = "not supported for recovery" }, func(r *api.ControlRequest) { r.Duration = "1m" }, func(r *api.ControlRequest) { r.IncidentID = "other" }, func(r *api.ControlRequest) { r.Resolution = "accepted" }, func(r *api.ControlRequest) { r.EvidenceRefs = []string{"ticket:1"} }} {
		r := base
		mutate(&r)
		if _, err := c.PrepareAction(context.Background(), "recover", m.ID, r); !errors.Is(err, persistence.ErrControlInvalid) {
			t.Fatal("invalid recovery accepted", err)
		}
	}
	for _, refs := range [][]string{{""}, {"same", "same"}, {strings.Repeat("é", 1025)}, {"line\nbreak"}, {"nul\x00"}, {"x", "y", "z", "a", "b", "c", "d", "e", "f"}} {
		if validEvidenceRefs(refs) {
			t.Fatal("unsafe evidence accepted")
		}
	}
	if !validEvidenceRefs([]string{"ticket:123", "https://evidence.invalid/receipt"}) {
		t.Fatal("opaque references rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PrepareAction(ctx, "recover", m.ID, base); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
func TestActionProjectionCopiesReviewAndKeepsProviderFacts(t *testing.T) {
	at := time.Now().UTC()
	a := persistence.ActionRecord{Action: persistence.Action{ID: "a", CatalogUID: "old-incarnation", Revision: "execution", State: persistence.Unknown, Outcome: "external_outcome_unknown", Review: &persistence.ActionReview{Revision: "review", Actor: "operator", Resolution: "accepted", At: at, Reason: "Receipt verified", EvidenceRefs: []string{"ticket:1"}}}, MonitorID: "m", ReviewRevision: "observation", ExecutorFenced: true}
	out := actionView(a)
	if out.State != "unknown" || out.Outcome != a.Outcome || out.IncarnationUID != "old-incarnation" || out.Held || out.CreatedAt != nil {
		t.Fatal("projection changed provider facts", out)
	}
	out.Review.EvidenceRefs[0] = "caller mutation"
	if a.Review.EvidenceRefs[0] != "ticket:1" {
		t.Fatal("review references aliased")
	}
}
