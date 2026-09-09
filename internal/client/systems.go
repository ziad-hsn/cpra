package client

import (
	"context"
)

// Systems returns the per-system and aggregate performance metrics.
func (c *Client) Systems(ctx context.Context) (*Systems, error) {
	var out Systems
	if err := c.get(ctx, "/api/v1/systems", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
