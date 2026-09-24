package management

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// reserveCommand runs only inside mutation admission, never preparation or dry
// run. The reservation binds the exact prepared payload and preconditions. An
// unconfirmed allocation submits no target command and supplies no invented ID.
// After confirmation, the caller retains the handle even if activation fails.
func (c *Catalog) reserveCommand(ctx context.Context, command *persistence.Command) (string, error) {
	command.At = time.Now().UTC()
	reservation, err := c.store.ReserveOperation(ctx, *command)
	if err != nil {
		return "", err
	}
	id := reservation.ID
	switch command.Kind {
	case "catalog":
		command.Catalog.OperationID = id
	case "control":
		command.Control.OperationID = id
	case "manual_recovery":
		command.ManualRecovery.OperationID = id
	case "action_review":
		command.ActionReview.OperationID = id
	}
	// Time is an observation from this admission, not part of the frozen target
	// digest. Eligibility and expiry must not use an old preparation timestamp.
	command.At = time.Now().UTC()
	return id, nil
}

// The receipt handle is published once while Commit waits for target admission.
// Concurrent readers see either no confirmed reservation or the complete handle.
func operationIDValue(value *atomic.Pointer[string]) string {
	if id := value.Load(); id != nil {
		return *id
	}
	return ""
}
