//go:build !externaljobs

package management

import (
	"context"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (c *Catalog) ConfigurationWarnings(_ context.Context, resource api.Resource) []api.FieldError {
	return api.ResourceWarnings(resource)
}

func (*Catalog) validateRecoveryRuleContract(context.Context, api.Resource) error { return nil }
