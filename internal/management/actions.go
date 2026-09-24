package management

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// PreparedAction freezes one action observation or one recovery dependency
// closure. It carries no provider configuration and grants no execution itself.
type PreparedAction struct {
	catalog     *Catalog
	command     persistence.Command
	operationID atomic.Pointer[string]
	used        atomic.Bool
}

func (p *PreparedAction) OperationID() string {
	if p == nil {
		return ""
	}
	return operationIDValue(&p.operationID)
}

type ActionResult struct {
	Action         api.Action
	Operation      api.Operation
	CommittedIndex uint64
}

func (c *Catalog) PrepareAction(ctx context.Context, action, id string, req api.ControlRequest) (*PreparedAction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.Ready() {
		return nil, ErrUnavailable
	}
	if !validID(id) || !validControlRevision(req.Revision) || !validControlText(req.Reason) || strings.TrimSpace(req.Reason) == "" || !validControlText(req.Note) || req.Duration != "" || req.IncidentID != "" {
		return nil, persistence.ErrControlInvalid
	}
	op := uuid.NewString()
	command := persistence.Command{At: time.Now().UTC()}
	switch action {
	case "review":
		if req.Resolution != "accepted" && req.Resolution != "rejected" && req.Resolution != "inconclusive" || !validEvidenceRefs(req.EvidenceRefs) {
			return nil, persistence.ErrControlInvalid
		}
		a, ok, err := c.store.Action(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, persistence.ErrCatalogNotFound
		}
		if a.ReviewRevision != req.Revision {
			return nil, persistence.ErrActionReviewConflict
		}
		command.Kind = "action_review"
		command.MonitorID = a.MonitorID
		command.ActionReview = &persistence.ActionReviewCommand{ActionID: id, MonitorUID: a.CatalogUID, ExpectedRevision: req.Revision, Revision: op, OperationID: op, Reason: req.Reason, Note: req.Note, Resolution: req.Resolution, EvidenceRefs: append([]string(nil), req.EvidenceRefs...)}
	case "recover":
		if req.Resolution != "" || req.Note != "" || len(req.EvidenceRefs) != 0 {
			return nil, persistence.ErrControlInvalid
		}
		guard, uid, err := c.recoveryGuard(ctx, id)
		if err != nil {
			return nil, err
		}
		command.Kind = "manual_recovery"
		command.MonitorID = id
		command.Guard = &guard
		command.ManualRecovery = &persistence.ManualRecoveryCommand{MonitorUID: uid, ExpectedControlRevision: req.Revision, Revision: op, OperationID: op, Reason: req.Reason}
	default:
		return nil, persistence.ErrControlInvalid
	}
	return &PreparedAction{catalog: c, command: command}, nil
}
func validEvidenceRefs(refs []string) bool {
	if len(refs) > 8 {
		return false
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref == "" || len(ref) > 2048 || !utf8.ValidString(ref) || seen[ref] {
			return false
		}
		for _, r := range ref {
			if unicode.IsControl(r) {
				return false
			}
		}
		seen[ref] = true
	}
	return true
}

// recoveryGuard reads only encrypted record identities and reference edges. A
// rotated target or credential invalidates admission without decrypting secrets.
func (c *Catalog) recoveryGuard(ctx context.Context, id string) (persistence.CatalogGuard, string, error) {
	view, err := c.store.CatalogSnapshot()
	if err != nil {
		return persistence.CatalogGuard{}, "", err
	}
	root := persistence.CatalogKey{Kind: "Monitor", ID: id}
	queue := []persistence.CatalogKey{root}
	seen := map[persistence.CatalogKey]bool{root: true}
	guard := persistence.CatalogGuard{}
	uid := ""
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return persistence.CatalogGuard{}, "", err
		}
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		record, ok := view.Get(key)
		if !ok {
			if key == root {
				return persistence.CatalogGuard{}, "", persistence.ErrCatalogNotFound
			}
			return persistence.CatalogGuard{}, "", persistence.ErrCatalogDependency
		}
		if key == root {
			uid = record.UID
		}
		guard.Conditions = append(guard.Conditions, persistence.CatalogCondition{Key: key, UID: record.UID, Revision: record.Revision})
		for _, ref := range record.References {
			if !seen[ref] {
				if len(seen) >= maxValidationGraph {
					return persistence.CatalogGuard{}, "", ErrGraphLimit
				}
				seen[ref] = true
				queue = append(queue, ref)
			}
		}
	}
	return guard, uid, nil
}
func (c *Catalog) CommitActionAs(ctx context.Context, p *PreparedAction, actor string) (ActionResult, error) {
	if p == nil || p.catalog != c || !p.used.CompareAndSwap(false, true) || actor == "" || len(actor) > 128 || !utf8.ValidString(actor) || strings.ContainsAny(actor, "\x00\r\n") {
		return ActionResult{}, persistence.ErrControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	if !c.Ready() {
		return ActionResult{}, ErrUnavailable
	}
	var results []persistence.Result
	var err error
	if p.command.ManualRecovery != nil {
		p.command.ManualRecovery.Actor = actor
	} else {
		p.command.ActionReview.Actor = actor
	}
	id, err := c.reserveCommand(ctx, &p.command)
	if err != nil {
		return ActionResult{}, err
	}
	p.operationID.Store(&id)
	if p.command.ManualRecovery != nil {
		results, err = c.store.RequestRecovery(ctx, p.command)
	} else {
		results, err = c.store.Submit(ctx, []persistence.Command{p.command})
	}
	if err != nil {
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			return ActionResult{}, fmt.Errorf("%w: %w", ErrOutcomeUnconfirmed, err)
		}
		if !c.store.Status().Ready {
			return ActionResult{}, ErrUnavailable
		}
		return ActionResult{}, err
	}
	if len(results) != 1 {
		return ActionResult{}, ErrOutcomeUnconfirmed
	}
	r := results[0]
	if r.Err != nil {
		return ActionResult{}, r.Err
	}
	if !r.Allowed || r.Operation == nil {
		return ActionResult{}, ErrOutcomeUnconfirmed
	}
	out := ActionResult{Operation: operationView(*r.Operation), CommittedIndex: r.Operation.CommittedIndex}
	if p.command.ActionReview != nil {
		if r.Monitor == nil {
			return ActionResult{}, ErrOutcomeUnconfirmed
		}
		a, ok := r.Monitor.Actions[p.command.ActionReview.ActionID]
		if !ok {
			return ActionResult{}, ErrOutcomeUnconfirmed
		}
		out.Action = actionView(persistence.ActionRecord{Action: a, MonitorID: p.command.MonitorID, ReviewRevision: persistence.ActionReviewRevision(a), Held: a.Held()})
		current, found, err := c.store.Action(a.ID)
		if err == nil && found && current.ReviewRevision == out.Action.ReviewRevision {
			out.Action.ExecutorFenced = current.ExecutorFenced
		}
	}
	return out, nil
}
