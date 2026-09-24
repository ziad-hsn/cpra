package cpra

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/internal/transport"
)

// Version identifies the SDK candidate, independently of the server API version.
const Version = "0.1.0-rc.1"

// DefaultMaxResponseBytes bounds an ordinary decoded response to 64 MiB.
const DefaultMaxResponseBytes int64 = 64 << 20

// TokenSource supplies credentials at request time, supporting explicit rotation.
type TokenSource func(context.Context) (string, error)

// Config is copied by New. Callers retain ownership of supplied transports.
// HTTPClient redirect policy is always replaced; no authorization follows redirects.
type Config struct {
	BaseURL           string
	AuthToken         string
	TokenSource       TokenSource
	Timeout           time.Duration
	HTTPClient        *http.Client
	RootCAs           *x509.CertPool
	AllowInsecureHTTP bool
	MaxResponseBytes  int64
	// ReadAttempts is opt-in, at most three. Mutations are never automatically retried.
	ReadAttempts int
}

// Client is safe for concurrent use after creation. Treat its services as immutable.
type Client struct {
	generated             *transport.Client
	http                  *http.Client
	doer                  *policyDoer
	maxBytes              int64
	base                  *url.URL
	Monitors              *MonitorsService
	NotificationEndpoints *NotificationEndpointsService
	NotificationGroups    *NotificationGroupsService
	Recipients            *RecipientsService
	Credentials           *CredentialsService
	Incidents             *IncidentsService
	Actions               *ActionsService
	Operations            *OperationsService
	Queues                *QueuesService
	Pools                 *PoolsService
	Systems               *SystemsService
	SLO                   *SLOService
}

// New validates connection settings and constructs a client without making a
// request. Caller-supplied HTTP clients are copied; their transport remains
// caller-owned. Redirects and automatic mutation retries are disabled.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("base URL must be an HTTP(S) origin and optional path, without user information, query, or fragment")
	}
	if (cfg.AuthToken != "" || cfg.TokenSource != nil) && base.Scheme != "https" && !cfg.AllowInsecureHTTP {
		return nil, errors.New("authenticated API requires HTTPS; explicitly allow insecure HTTP for this origin if needed")
	}
	if cfg.AuthToken != "" && cfg.TokenSource != nil {
		return nil, errors.New("configure one authentication source")
	}
	if cfg.ReadAttempts < 0 || cfg.ReadAttempts > 3 {
		return nil, errors.New("read attempts must be between zero and three")
	}
	if cfg.Timeout < 0 || (cfg.MaxResponseBytes < 0 || cfg.MaxResponseBytes > 1<<40) {
		return nil, errors.New("timeout and response bound cannot be negative")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if cfg.ReadAttempts == 0 {
		cfg.ReadAttempts = 1
	}
	hc := &http.Client{}
	if cfg.HTTPClient != nil {
		*hc = *cfg.HTTPClient
	}
	hc.Timeout = 0 // Deadline wraps the body lifetime and supports bounded long polls.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Transport == nil {
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			hc.Transport = tr.Clone()
		} else {
			hc.Transport = http.DefaultTransport
		}
	}
	if cfg.RootCAs != nil {
		tr, ok := hc.Transport.(*http.Transport)
		if !ok {
			return nil, errors.New("custom trust roots require an http.Transport")
		}
		tr = tr.Clone()
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		}
		tr.TLSClientConfig.RootCAs = cfg.RootCAs.Clone()
		hc.Transport = tr
	}
	d := &policyDoer{client: hc, base: base, token: cfg.AuthToken, tokens: cfg.TokenSource, timeout: cfg.Timeout, attempts: cfg.ReadAttempts}
	gen, err := transport.NewClient(strings.TrimRight(base.String(), "/"), transport.WithHTTPClient(d))
	if err != nil {
		return nil, err
	}
	c := &Client{generated: gen, http: hc, doer: d, maxBytes: cfg.MaxResponseBytes, base: base}
	c.Monitors = &MonitorsService{c}
	c.NotificationEndpoints = &NotificationEndpointsService{c}
	c.NotificationGroups = &NotificationGroupsService{c}
	c.Recipients = &RecipientsService{c}
	c.Credentials = &CredentialsService{c}
	c.Incidents = &IncidentsService{c}
	c.Actions = &ActionsService{c}
	c.Operations = &OperationsService{c}
	c.Queues = &QueuesService{c}
	c.Pools = &PoolsService{c}
	c.Systems = &SystemsService{c}
	c.SLO = &SLOService{c}
	return c, nil
}

// Response retains identity/progress headers separately from the typed payload.
type Response[T any] struct {
	Data            T
	RequestID       string
	ResourceVersion string
	OperationID     string
	StatusCode      int
	RetryAfter      time.Duration
}

type policyDoer struct {
	client   *http.Client
	base     *url.URL
	token    string
	tokens   TokenSource
	timeout  time.Duration
	attempts int
}
type timeoutKey struct{}
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }
func (d *policyDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != d.base.Scheme || req.URL.Host != d.base.Host {
		return nil, &localError{errors.New("refusing request outside configured origin")}
	}
	timeout := d.timeout
	if v, ok := req.Context().Value(timeoutKey{}).(time.Duration); ok {
		timeout = v
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	req = req.Clone(ctx)
	token := d.token
	if d.tokens != nil {
		var err error
		token, err = d.tokens(ctx)
		if err != nil {
			cancel()
			return nil, &localError{errors.New("authentication token source failed")}
		}
	}
	if (d.tokens != nil && token == "") || strings.ContainsAny(token, "\r\n") {
		cancel()
		return nil, &localError{errors.New("invalid token")}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	req.Header.Set("User-Agent", "cpra-go/"+Version)
	attempts := 1
	if req.Method == http.MethodGet {
		attempts = d.attempts
	}
	for attempt := 0; attempt < attempts; attempt++ {
		resp, err := d.client.Do(req)
		if err != nil {
			cancel()
			return nil, &TransportError{Cause: err}
		}
		if attempt+1 == attempts || (resp.StatusCode != 429 && resp.StatusCode != 502 && resp.StatusCode != 503 && resp.StatusCode != 504) {
			resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}
		delay := retryDelay(resp.Header.Get("Retry-After"))
		_ = resp.Body.Close()
		if delay == 0 {
			delay = time.Duration(attempt+1) * 100 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	cancel()
	return nil, errors.New("request attempts exhausted")
}
func retryDelay(s string) time.Duration {
	var d time.Duration
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		if n >= 30 {
			return 30 * time.Second
		}
		d = time.Duration(n) * time.Second
	} else if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(s, "-") {
		return 30 * time.Second
	} else if t, err := http.ParseTime(s); err == nil {
		d = time.Until(t)
	}
	if d < 0 {
		return 0
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
func boundedRead(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}
func response[T any](c *Client, resp *http.Response, err error, mutation bool) (*Response[T], error) {
	return responseBounded[T](c, resp, err, mutation, c.maxBytes)
}

// responseBounded keeps a stricter endpoint ceiling without changing the caller's client.
func responseBounded[T any](c *Client, resp *http.Response, err error, mutation bool, maxBytes int64) (*Response[T], error) {
	if err != nil {
		var local *localError
		if errors.As(err, &local) {
			return nil, local
		}
		var te *TransportError
		if mutation && errors.As(err, &te) {
			return nil, &AmbiguousError{Cause: err}
		}
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("empty transport response")
	}
	defer resp.Body.Close()
	version := resp.Header.Get("ETag")
	if len(version) >= 2 && version[0] == '"' && version[len(version)-1] == '"' && precondition(version[1:len(version)-1]) == nil {
		version = version[1 : len(version)-1]
	}
	r := &Response[T]{RequestID: resp.Header.Get("X-Request-ID"), ResourceVersion: version, OperationID: resp.Header.Get("X-Operation-ID"), StatusCode: resp.StatusCode, RetryAfter: retryDelay(resp.Header.Get("Retry-After"))}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, readErr := boundedRead(resp.Body, 64<<10)
		p := api.Problem{}
		if readErr == nil {
			_ = json.Unmarshal(raw, &p)
		}
		e := &Error{StatusCode: resp.StatusCode, Problem: p, RequestID: r.RequestID, OperationID: r.OperationID}
		e.notAdmitted = mutation && readErr == nil && allocationNotSubmitted(resp, raw)
		if mutation && resp.StatusCode >= 500 {
			if e.notAdmitted {
				return r, e
			}
			return r, &AmbiguousError{Cause: e, OperationID: r.OperationID}
		}
		return r, e
	}
	raw, err := boundedRead(resp.Body, min(c.maxBytes, maxBytes))
	if err == nil {
		err = api.DecodeResponse(raw, &r.Data)
	}
	if err != nil {
		if mutation {
			return r, &AmbiguousError{Cause: err, OperationID: r.OperationID}
		}
		return r, err
	}
	return r, nil
}

// Metrics returns the structured diagnostic snapshot.
func (c *Client) Metrics(ctx context.Context) (*Response[api.Metrics], error) {
	r, e := c.generated.GetMetrics(ctx)
	return response[api.Metrics](c, r, e, false)
}

// Prometheus streams the metrics representation without accumulating it in memory.
// The bound detects excess data rather than silently returning a truncated success.
func (c *Client) Prometheus(ctx context.Context, w io.Writer) error {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	r, err := c.doer.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return &Error{StatusCode: r.StatusCode}
	}
	n, err := io.Copy(w, io.LimitReader(r.Body, c.maxBytes))
	if err != nil {
		return err
	}
	if n == c.maxBytes {
		var probe [1]byte
		n, e := r.Body.Read(probe[:])
		if n > 0 {
			return ErrResponseTooLarge
		}
		if e != nil && e != io.EOF {
			return e
		}
	}
	return nil
}

// CloseIdleConnections releases idle HTTP connections; it does not cancel calls.
// CloseIdleConnections closes idle transport connections without interrupting
// requests already in flight. It does not cancel operations on the server.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }
func validID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#%\r\n") {
		return errors.New("invalid resource identity")
	}
	return nil
}
func precondition(version string) error {
	if version == "" || len(version) > 256 || strings.HasPrefix(version, "W/") || strings.ContainsAny(version, "\"\\,*") {
		return errors.New("an explicit resource version is required")
	}
	for _, b := range []byte(version) {
		if b < 0x21 || b > 0x7e {
			return errors.New("invalid resource version")
		}
	}
	return nil
}

// strongETag quotes an already validated opaque revision for the HTTP wire.
// Public methods accept resourceVersion values, never weak tags or tag lists.
func strongETag(version string) string     { return "\"" + version + "\"" }
func control(req api.ControlRequest) error { return precondition(req.Revision) }

// ListOptions never requests an implicit full-fleet response.
type ListOptions struct {
	Cursor    string
	Limit     int
	Selector  string
	MonitorID string
}

func (o ListOptions) validate() error {
	if o.Limit < 0 || o.Limit > 500 {
		return errors.New("page limit must be between 1 and 500")
	}
	if len(o.Selector) > 1024 {
		return errors.New("selector exceeds 1 KiB")
	}
	return nil
}
func (o ListOptions) limit() int {
	if o.Limit == 0 {
		return 100
	}
	return o.Limit
}
func ptr[T any](v T) *T { return &v }

// Iterator retains one page and fetches the next page only when exhausted.
type Iterator[T any] struct {
	fetch         func(context.Context, string) ([]T, string, error)
	items         []T
	next          string
	index         int
	started, done bool
	value         T
	err           error
}

// Next advances to the next item, fetching at most one page as needed. It returns
// false at the end or after an error; inspect Err to distinguish those cases.
func (i *Iterator[T]) Next(ctx context.Context) bool {
	if i.err != nil || i.done {
		return false
	}
	for i.index >= len(i.items) {
		if i.started && i.next == "" {
			i.done = true
			return false
		}
		previous := i.next
		items, next, err := i.fetch(ctx, i.next)
		if err != nil {
			i.err = err
			return false
		}
		if i.started && next != "" && next == previous {
			i.err = errors.New("server repeated pagination cursor")
			return false
		}
		i.items = items
		i.next = next
		i.index = 0
		i.started = true
		if len(items) == 0 && next != "" {
			i.err = errors.New("server returned an empty nonterminal page")
			return false
		}
	}
	i.value = i.items[i.index]
	i.index++
	return true
}

// Value returns the item selected by the most recent successful Next call.
func (i *Iterator[T]) Value() T { return i.value }

// Err returns the first paging or context error, or nil after normal completion.
func (i *Iterator[T]) Err() error { return i.err }
