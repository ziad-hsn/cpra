package client

import (
	"context"
)

// Queues returns the current queue statistics, keyed by queue name
// (pulse, intervention, code).
func (c *Client) Queues(ctx context.Context) (map[string]QueueStats, error) {
	var out map[string]QueueStats
	if err := c.get(ctx, "/api/v1/queues", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// QueuesHistory returns the rolling window of queue statistics, keyed by
// queue name.
func (c *Client) QueuesHistory(ctx context.Context) (QueuesHistory, error) {
	var out QueuesHistory
	if err := c.get(ctx, "/api/v1/queues/history", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Pools returns the current worker-pool statistics, keyed by pool name
// (pulse, intervention, code).
func (c *Client) Pools(ctx context.Context) (map[string]PoolStats, error) {
	var out map[string]PoolStats
	if err := c.get(ctx, "/api/v1/pools", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PoolsHistory returns the rolling window of worker-pool statistics, keyed by
// pool name.
func (c *Client) PoolsHistory(ctx context.Context) (PoolsHistory, error) {
	var out PoolsHistory
	if err := c.get(ctx, "/api/v1/pools/history", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
