package client

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"cpra/internal/web/snapshot"
)

// MonitorListOptions filters and paginates the /api/v1/monitors request.
type MonitorListOptions struct {
	Status    string // up | down | verifying | incident | disabled
	PulseType string // http | tcp | icmp | dns | udp | grpc | docker
	Code      string // red | yellow | green | cyan | gray
	Query     string // substring match on monitor name
	Page      int    // 1-based; default 1
	Size      int    // page size; default 50, max 500
}

func (o MonitorListOptions) values() url.Values {
	v := url.Values{}
	if o.Status != "" {
		v.Set("status", o.Status)
	}
	if o.PulseType != "" {
		v.Set("type", o.PulseType)
	}
	if o.Code != "" {
		v.Set("code", o.Code)
	}
	if o.Query != "" {
		v.Set("q", o.Query)
	}
	if o.Page > 0 {
		v.Set("page", strconv.Itoa(o.Page))
	}
	if o.Size > 0 {
		v.Set("size", strconv.Itoa(o.Size))
	}
	return v
}

// Overview returns the fleet overview.
func (c *Client) Overview(ctx context.Context) (*Overview, error) {
	var out Overview
	if err := c.get(ctx, "/api/v1/overview", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMonitors returns a filtered, paginated list of monitors.
func (c *Client) ListMonitors(ctx context.Context, opts MonitorListOptions) (*MonitorsList, error) {
	var out MonitorsList
	if err := c.get(ctx, "/api/v1/monitors", opts.values(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetMonitor returns a single monitor by entity ID.
func (c *Client) GetMonitor(ctx context.Context, id uint32) (*snapshot.MonitorSummary, error) {
	var out snapshot.MonitorSummary
	if err := c.get(ctx, fmt.Sprintf("/api/v1/monitors/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListIncidents returns the current incidents.
func (c *Client) ListIncidents(ctx context.Context) (*Incidents, error) {
	var out Incidents
	if err := c.get(ctx, "/api/v1/incidents", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
