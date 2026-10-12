package cpra

import (
	"context"
	"errors"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// DiscoverAccess reads the non-sensitive process access mode. It grants no
// authority. A subsequent authentication failure is never a reason to downgrade
// management writes to observation behavior.
func (c *Client) DiscoverAccess(ctx context.Context) (*Response[api.AccessDiscovery], error) {
	resp, err := c.generated.DiscoverAccess(ctx)
	result, err := response[api.AccessDiscovery](c, resp, err, false)
	if err == nil && !result.Data.Mode.Valid() {
		return result, errors.New("unsupported process access mode")
	}
	return result, err
}
