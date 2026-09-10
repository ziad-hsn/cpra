package client

import (
	"context"
	"net/url"
	"strconv"

	"cpra/internal/durable"
	"cpra/internal/slo"
)

type State struct {
	StorageUsage durable.DiskUsage `json:"storage_usage"`
	Process      map[string]uint64 `json:"process"`
	Storage      durable.Status    `json:"storage"`
	MonitorID    string            `json:"monitor_id,omitempty"`
	Revision     string            `json:"revision,omitempty"`
	Actions      []durable.Action  `json:"actions"`
}

func (c *Client) History(ctx context.Context, monitorID, cursor string, limit int) (*durable.HistoryPage, error) {
	var out durable.HistoryPage
	query := url.Values{"monitor_id": {monitorID}, "cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	if err := c.get(ctx, "/api/v1/history", query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) State(ctx context.Context, monitorID string) (*State, error) {
	var out State
	if err := c.get(ctx, "/api/v1/state", url.Values{"monitor_id": {monitorID}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Client) SLO(ctx context.Context) (*slo.View, error) {
	var out slo.View
	if err := c.get(ctx, "/api/v1/slo", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
