package management

import (
	"context"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// resourceLookup lends an immutable resource only during the callback. Neither
// the resource nor its reference-bearing fields may be retained or modified.
// An absent key returns false without calling visit. Implementations authorize
// identities before querying existence or decrypting their data; callers remain
// responsible for authorizing any already-materialized graph supplied to a map.
type resourceLookup interface {
	withResource(context.Context, persistence.CatalogKey, func(*api.Resource) error) (bool, error)
}

// reserveResourceScratch reserves a conservative byte allowance before decoded
// or encoded resource copies are allocated. A successful reservation returns a
// release function, called exactly once after the caller's last use. Errors must
// not contain resource data. Nil disables accounting for existing map callers.
// This bounds accounted copies, not total Go heap or RSS. Borrowed source bytes
// have their own lifetime and are charged by the lookup implementation.
type reserveResourceScratch func(int) (release func(), err error)

// mapResourceLookup adapts an already-authorized immutable graph. It borrows the
// map's data without copying specs and must not race a map or resource writer.
type mapResourceLookup map[persistence.CatalogKey]api.Resource

func (m mapResourceLookup) withResource(ctx context.Context, key persistence.CatalogKey, visit func(*api.Resource) error) (bool, error) {
	if ctx == nil || visit == nil {
		return false, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r, exists := m[key]
	if !exists {
		return false, nil
	}
	if err := visit(&r); err != nil {
		return true, err
	}
	return true, ctx.Err()
}
