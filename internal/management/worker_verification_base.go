//go:build !externaljobs

package management

import "context"

func (*Catalog) verifyWorkerExecutions(ctx context.Context) error { return ctx.Err() }
