package persistence

import (
	"context"
	"slices"
	"strings"
)

// PendingOperations copies only the bounded active receipt ledger, never
// resource bodies or history. It is for the background projection reconciler.
// Each receipt must still be conditionally completed against its original
// resource identity after the world owner acknowledges application.
func (s *Store) PendingOperations() ([]OperationReceipt, error) {
	return s.PendingOperationsContext(context.Background())
}

// PendingOperationsContext copies the bounded active receipt ledger within ctx.
func (s *Store) PendingOperationsContext(ctx context.Context) ([]OperationReceipt, error) {
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	receipts := make([]OperationReceipt, 0, len(s.fsm.image.Operations))
	for _, receipt := range s.fsm.image.Operations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	slices.SortFunc(receipts, func(a, b OperationReceipt) int { return strings.Compare(a.ID, b.ID) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return receipts, nil
}
