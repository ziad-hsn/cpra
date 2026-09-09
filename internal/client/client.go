// Package client is a typed HTTP client for the CPRA read-only API. It is the
// backend for the cpractl CLI: it knows the /api/v1 endpoints and their JSON
// shapes, and exposes them as Go methods. It has no CLI concerns (no Cobra,
// no output formatting) so it can be reused by other tools and tested in
// isolation.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBytes bounds the size of a 2xx response body we will read, so a
// misbehaving server cannot make the CLI allocate unbounded memory.
const maxResponseBytes = 64 << 20 // 64 MiB

// Client is a typed HTTP client for the CPRA read-only API.
type Client struct {
	authToken  string
	baseURL    *url.URL
	httpClient *http.Client
}

// Config configures a Client.
type Config struct {
	AuthToken string
	// BaseURL is the CPRA web server address, e.g. "http://localhost:8060".
	BaseURL string
	// Timeout is the per-request timeout. Defaults to 10s when <= 0.
	Timeout time.Duration
}

// New returns a Client for the given base URL.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL %q: %w", cfg.BaseURL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("base URL must include scheme and host: %q", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL:    base,
		authToken:  cfg.AuthToken,
		httpClient: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

// Error is a non-2xx API response.
type Error struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("GET %s: status %d: %s", e.Path, e.StatusCode, e.Body)
}

// get performs a GET request against path with the given query and decodes the
// JSON response into out. When out is nil, the body is not decoded.
func (c *Client) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	body, err := c.getRaw(ctx, path, query)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

// getRaw performs a GET request and returns the raw response body as a string.
func (c *Client) getRaw(ctx context.Context, path string, query url.Values) (string, error) {
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	if len(query) > 0 {
		if u.RawQuery == "" {
			u.RawQuery = query.Encode()
		} else {
			u.RawQuery += "&" + query.Encode()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &Error{StatusCode: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
