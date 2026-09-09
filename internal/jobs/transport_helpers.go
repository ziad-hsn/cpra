package jobs

import (
	"context"
	"net"
	"net/url"
	"regexp"
	"time"
)

func remaining(ctx context.Context) time.Duration {
	d, _ := ctx.Deadline()
	t := time.Until(d)
	if t <= 0 {
		return time.Nanosecond
	}
	return t
}
func retryDelay(ctx context.Context) bool {
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}

type ownedConn struct {
	net.Conn
	stop func() bool
}

func (c *ownedConn) Close() error { c.stop(); return c.Conn.Close() }
func dialBounded(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if d, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	return &ownedConn{c, stop}, nil
}

var errorURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s<>"']+`)
var errorCredential = regexp.MustCompile(`(?i)(password|passwd|token|api[_-]?key|secret|authorization)\s*[=:]\s*([^\s,;]+)`)

type safeError struct {
	original error
	message  string
}

func (e safeError) Error() string { return e.message }
func (e safeError) Unwrap() error { return e.original }

// SanitizeError preserves error identity without credentials in URL userinfo,
// paths, query strings or fragments. Webhook keys commonly live in URL paths.
func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	s := errorURL.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[redacted URL]"
		}
		return u.Scheme + "://" + u.Host + "/[redacted]"
	})
	s = errorCredential.ReplaceAllString(s, "$1=[redacted]")
	return safeError{err, s}
}
