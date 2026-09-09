package client

import (
	"context"
)

// Config returns the runtime configuration.
func (c *Client) Config(ctx context.Context) (*RuntimeConfig, error) {
	var out RuntimeConfig
	if err := c.get(ctx, "/api/v1/config", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Health checks the /api/v1/healthz endpoint and returns nil when healthy.
func (c *Client) Health(ctx context.Context) error {
	return c.get(ctx, "/api/v1/healthz", nil, nil)
}

// Metrics returns the raw Prometheus text from /metrics.
func (c *Client) Metrics(ctx context.Context) (string, error) {
	return c.getRaw(ctx, "/metrics", nil)
}
