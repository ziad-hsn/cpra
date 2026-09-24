//go:build !externaljobs

package management

import "context"

func (*Catalog) verifyJobTypes(ctx context.Context) error { return ctx.Err() }
