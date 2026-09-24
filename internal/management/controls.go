package management

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// PreparedControl freezes the exact monitor incarnation, incident and revision.
// It contains no executable work and can be admitted only once.
type PreparedControl struct {
	catalog     *Catalog
	command     persistence.Command
	operationID atomic.Pointer[string]
	used        atomic.Bool
}

func (p *PreparedControl) OperationID() string {
	if p == nil {
		return ""
	}
	return operationIDValue(&p.operationID)
}

type ControlResult struct {
	Incident       api.Incident
	Operation      api.Operation
	CommittedIndex uint64
}

// PrepareControl validates an operator request without changing durable state.
// The authenticated actor is supplied separately at the commit boundary.
func (c *Catalog) PrepareControl(ctx context.Context, action, id string, req api.ControlRequest) (*PreparedControl, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.Ready() {
		return nil, ErrUnavailable
	}
	if !validID(id) || !validControlRevision(req.Revision) || len(req.EvidenceRefs) != 0 || req.Resolution != "" ||
		!validControlText(req.Note) || !validControlText(req.Reason) {
		return nil, persistence.ErrControlInvalid
	}
	at := time.Now().UTC()
	control := &persistence.ControlCommand{Action: action, ExpectedRevision: req.Revision, Revision: uuid.NewString(), Reason: req.Reason, Note: req.Note}
	control.OperationID = control.Revision
	monitorID := id
	switch action {
	case "acknowledge", "dismiss", "reopen":
		if req.Duration != "" || (req.IncidentID != "" && req.IncidentID != id) || (action == "dismiss" && strings.TrimSpace(req.Reason) == "") {
			return nil, persistence.ErrControlInvalid
		}
		incident, ok, err := c.store.Incident(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, persistence.ErrCatalogNotFound
		}
		monitorID, control.MonitorUID, control.IncidentID = incident.MonitorID, incident.MonitorUID, id
	case "snooze", "unsnooze":
		if req.IncidentID != "" {
			return nil, persistence.ErrControlInvalid
		}
		if action == "snooze" {
			duration, err := time.ParseDuration(req.Duration)
			if err != nil || duration <= 0 || duration > persistence.MaxSnoozeDuration || strings.TrimSpace(req.Reason) == "" {
				return nil, persistence.ErrControlInvalid
			}
			control.Until = at.Add(duration)
		} else if req.Duration != "" {
			return nil, persistence.ErrControlInvalid
		}
		status, ok, err := c.store.MonitorStatus(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, persistence.ErrCatalogNotFound
		}
		control.MonitorUID = status.CatalogUID
	default:
		return nil, persistence.ErrControlInvalid
	}
	// A state record from a deleted/recreated Monitor never grants control over
	// its replacement, even if it remains available in incident history.
	record, ok, err := c.store.CatalogGet(persistence.CatalogKey{Kind: "Monitor", ID: monitorID})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, persistence.ErrCatalogNotFound
	}
	if record.UID != control.MonitorUID {
		return nil, persistence.ErrControlConflict
	}
	return &PreparedControl{catalog: c, command: persistence.Command{Kind: "control", MonitorID: monitorID, At: at, Control: control}}, nil
}

func validControlText(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r")
}
func validControlRevision(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e || strings.ContainsRune("\"\\,*", rune(b)) {
			return false
		}
	}
	return true
}

// CommitControlAs commits one prepared control with the authenticated actor.
// It reports durable admission, never completion of the owner-loop projection.
func (c *Catalog) CommitControlAs(ctx context.Context, p *PreparedControl, actor string) (ControlResult, error) {
	if p == nil || p.catalog != c || !p.used.CompareAndSwap(false, true) || actor == "" || len(actor) > 128 ||
		!utf8.ValidString(actor) || strings.ContainsAny(actor, "\x00\r\n") {
		return ControlResult{}, persistence.ErrControlInvalid
	}
	if err := ctx.Err(); err != nil {
		return ControlResult{}, err
	}
	if !c.Ready() {
		return ControlResult{}, ErrUnavailable
	}
	p.command.Control.Actor = actor
	id, err := c.reserveCommand(ctx, &p.command)
	if err != nil {
		return ControlResult{}, err
	}
	p.operationID.Store(&id)
	results, err := c.store.Submit(ctx, []persistence.Command{p.command})
	if err != nil {
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			return ControlResult{}, fmt.Errorf("%w: %w", ErrOutcomeUnconfirmed, err)
		}
		if !c.store.Status().Ready {
			return ControlResult{}, ErrUnavailable
		}
		return ControlResult{}, err
	}
	if len(results) != 1 {
		return ControlResult{}, ErrOutcomeUnconfirmed
	}
	result := results[0]
	if result.Err != nil {
		return ControlResult{}, result.Err
	}
	if !result.Allowed || result.Monitor == nil || result.Operation == nil {
		return ControlResult{}, ErrOutcomeUnconfirmed
	}
	return ControlResult{Incident: incidentFromMonitor(*result.Monitor), Operation: operationView(*result.Operation), CommittedIndex: result.Operation.CommittedIndex}, nil
}
